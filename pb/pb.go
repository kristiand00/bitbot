package pb

import (
	"bitbot/pb/migrations"
	"database/sql"
	"encoding/json" // For unmarshalling target_user_ids fallback
	"errors"
	"fmt"
	"reflect"
	"sync"
	"time" // Added for reminder timestamps

	"github.com/charmbracelet/log"
	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/core"
)

// isNotFound reports whether err is PocketBase's "record not found" error.
// The record lookup helpers (FindRecordById, FindFirstRecordByFilter) return
// sql.ErrNoRows when nothing matches, so match on that sentinel rather than on
// the error text, which is brittle across versions.
func isNotFound(err error) bool {
	return errors.Is(err, sql.ErrNoRows)
}

var (
	appOnce sync.Once
	pbApp   *pocketbase.PocketBase // Renamed from 'app' to avoid conflict if any package level 'app' existed
)

var reminderLocation *time.Location

func init() {
	var err error
	reminderLocation, err = time.LoadLocation("Europe/Zagreb")
	if err != nil {
		reminderLocation = time.UTC
	}
}

type ServerInfo struct {
	UserID            string
	GuildID           string
	ConnectionDetails string
}

// Reminder struct corresponds to the 'reminders' collection schema
type Reminder struct {
	ID               string    `db:"id" json:"id"` // PocketBase record ID
	UserID           string    `db:"user_id" json:"user_id"`
	TargetUserIDs    []string  `db:"target_user_ids" json:"target_user_ids"` // Stored as JSON in PB
	Message          string    `db:"message" json:"message"`
	ChannelID        string    `db:"channel_id" json:"channel_id"`
	GuildID          string    `db:"guild_id" json:"guild_id,omitempty"`
	ReminderTime     time.Time `db:"reminder_time" json:"reminder_time"`                     // Specific time for the reminder
	IsRecurring      bool      `db:"is_recurring" json:"is_recurring"`                       // Is it a recurring reminder?
	RecurrenceRule   string    `db:"recurrence_rule" json:"recurrence_rule,omitempty"`       // e.g., "daily", "weekly"
	NextReminderTime time.Time `db:"next_reminder_time" json:"next_reminder_time,omitempty"` // Next time for recurring
	LastTriggeredAt  time.Time `db:"last_triggered_at" json:"last_triggered_at,omitempty"`   // Last time it was triggered
	CreatedAt        time.Time `db:"created" json:"created"`                                 // PocketBase managed
	UpdatedAt        time.Time `db:"updated" json:"updated"`                                 // PocketBase managed
}

// Init initializes the PocketBase app
func Init() {
	appOnce.Do(func() {
		pbApp = pocketbase.New()

		// Bootstrap the app (important for running as a library)
		if err := pbApp.Bootstrap(); err != nil {
			log.Fatalf("Failed to bootstrap PocketBase: %v", err)
		}

		// Ensure collections exist
		if err := ensureCollectionsExist(); err != nil {
			log.Fatalf("Failed to ensure collections exist: %v", err)
		}

		// Note: pbApp.Start() is not called here as we are using PocketBase as a library,
		// not running its full HTTP server. Bootstrap prepares it for DAO operations.
		log.Info("PocketBase initialized and bootstrapped.")
	})
}

// ensureCollectionsExist runs all pending schema/data migrations (see the
// migrations package). Required-collection failures return an error (fatal at
// startup); optional ones are logged and skipped inside the runner.
func ensureCollectionsExist() error {
	return migrations.Run(GetApp())
}

// MCP visibility levels controlling who (besides the owner) may use a server's
// tools.
const (
	MCPVisibilityPrivate = "private" // owner only
	MCPVisibilityAdmins  = "admins"  // owner + any admin
	MCPVisibilityPublic  = "public"  // everyone
)

// MCPServer describes a remote MCP server whose tools are exposed through the
// bot's toolbelt.
type MCPServer struct {
	ID      string
	Name    string
	URL     string
	Token   string
	Enabled bool
	// Owner is the Discord user ID that added the server (empty for legacy/system
	// servers). Its token is used for all calls to the server.
	Owner string
	// Visibility is one of MCPVisibility{Private,Admins,Public}.
	Visibility string
	// AuthMode is MCPAuth{Bearer,OAuth}: how the server authenticates.
	AuthMode string
}

const mcpServersCollection = "mcp_servers"

func normalizeVisibility(v string) string {
	switch v {
	case MCPVisibilityAdmins, MCPVisibilityPublic:
		return v
	default:
		return MCPVisibilityPrivate
	}
}

// ListMCPServers returns all configured MCP servers.
func ListMCPServers() ([]*MCPServer, error) {
	currentApp := GetApp()
	records, err := currentApp.FindAllRecords(mcpServersCollection)
	if err != nil {
		if isNotFound(err) {
			return []*MCPServer{}, nil
		}
		log.Error("Error listing MCP servers", "error", err)
		return nil, err
	}
	servers := make([]*MCPServer, 0, len(records))
	for _, r := range records {
		servers = append(servers, &MCPServer{
			ID:         r.Id,
			Name:       r.GetString("name"),
			URL:        r.GetString("url"),
			Token:      r.GetString("token"),
			Enabled:    r.GetBool("enabled"),
			Owner:      r.GetString("owner"),
			Visibility: normalizeVisibility(r.GetString("visibility")),
			AuthMode:   normalizeAuthMode(r.GetString("auth_mode")),
		})
	}
	return servers, nil
}

// AddMCPServer inserts a new MCP server owned by owner. It is a no-op (returns
// false) if the same owner already has a server with that URL. Visibility
// controls who besides the owner may use its tools.
func AddMCPServer(name, url, token, owner, visibility string) (bool, error) {
	currentApp := GetApp()

	if existing, _ := currentApp.FindFirstRecordByFilter(
		mcpServersCollection, "url = {:url} && owner = {:owner}",
		dbx.Params{"url": url, "owner": owner},
	); existing != nil {
		return false, nil
	}

	collection, err := currentApp.FindCollectionByNameOrId(mcpServersCollection)
	if err != nil {
		return false, err
	}
	record := core.NewRecord(collection)
	record.Set("name", name)
	record.Set("url", url)
	record.Set("token", token)
	record.Set("enabled", true)
	record.Set("owner", owner)
	record.Set("visibility", normalizeVisibility(visibility))
	if err := currentApp.Save(record); err != nil {
		return false, err
	}
	return true, nil
}

// findOwnedOrLegacy locates a server by name that the caller may manage: one they
// own, or a legacy/unowned one. Returns nil if none match.
func findOwnedOrLegacy(name, caller string) *core.Record {
	currentApp := GetApp()
	// Prefer the caller's own server over a legacy one with the same name.
	rec, err := currentApp.FindFirstRecordByFilter(
		mcpServersCollection, "name = {:name} && owner = {:owner}",
		dbx.Params{"name": name, "owner": caller},
	)
	if err == nil && rec != nil {
		return rec
	}
	rec, err = currentApp.FindFirstRecordByFilter(
		mcpServersCollection, "name = {:name} && owner = ''",
		dbx.Params{"name": name},
	)
	if err == nil {
		return rec
	}
	return nil
}

// SetMCPServerVisibility updates the visibility of a server the caller may manage.
// Returns whether a matching server was found.
func SetMCPServerVisibility(name, caller, visibility string) (bool, error) {
	record := findOwnedOrLegacy(name, caller)
	if record == nil {
		return false, nil
	}
	record.Set("visibility", normalizeVisibility(visibility))
	if err := GetApp().Save(record); err != nil {
		return false, err
	}
	return true, nil
}

// RemoveMCPServer deletes a server the caller may manage. Not-found is a no-op.
func RemoveMCPServer(name, caller string) error {
	record := findOwnedOrLegacy(name, caller)
	if record == nil {
		return nil
	}
	return GetApp().Delete(record)
}

// GetApp is a helper to ensure pbApp is initialized.
func GetApp() *pocketbase.PocketBase {
	if pbApp == nil {
		// This case should ideally not be hit if Init() is called at application startup.
		log.Warn("PocketBase app (pbApp) requested before Init() was called. Calling Init() now.")
		Init()            // Ensures initialization
		if pbApp == nil { // If Init failed fatally (though it logs Fatalf)
			log.Fatal("pbApp is nil even after Init(). This indicates a critical error during bootstrap.")
			return nil // Should be unreachable
		}
	}
	return pbApp
}

func GetRecordById(collectionNameOrId string, recordID string) (*core.Record, error) {
	currentApp := GetApp()
	// Access PocketBase operations directly on the app instance
	record, err := currentApp.FindRecordById(collectionNameOrId, recordID) // Removed .Dao()
	if err != nil {
		log.Error("Error finding record by ID", "collection", collectionNameOrId, "recordID", recordID, "error", err)
		return nil, err
	}
	return record, nil
}

// CreateRecord creates a new record in the specified collection
func CreateRecord(collectionNameOrId string, data *ServerInfo) error {
	currentApp := GetApp()
	// Find the collection
	collection, err := currentApp.FindCollectionByNameOrId(collectionNameOrId) // Removed .Dao()
	if err != nil {
		log.Error("Error finding collection", "collection", collectionNameOrId, "error", err)
		return err
	}

	// Check if the server already exists
	// Note: For FindFirstRecordByFilter, ensure dbx.Params are correctly used if placeholders are complex.
	// The filter string must match placeholder syntax if used, e.g., "{:userID}" or standard SQL "?".
	// For dbx.Params, named placeholders like {:name} are common.
	existingRecord, _ := currentApp.FindFirstRecordByFilter( // Removed .Dao()
		collectionNameOrId,
		"UserID = {:userID} && GuildID = {:guildID} && ConnectionDetails = {:connectionDetails}", // Using named placeholders
		dbx.Params{"userID": data.UserID, "guildID": data.GuildID, "connectionDetails": data.ConnectionDetails},
	)
	// Not checking error for FindFirstRecordByFilter here, as a "not found" is not an error for this logic.
	// A nil existingRecord means it's safe to create.

	if existingRecord != nil {
		log.Info("Server record already exists for this user in this guild.", "userID", data.UserID, "guildID", data.GuildID, "details", data.ConnectionDetails)
		return nil // Not an error, just already exists
	}

	// Create a new record
	record := core.NewRecord(collection) // Use core.NewRecord

	// Set fields using Set() method for type safety and internal handling
	record.Set("UserID", data.UserID)
	record.Set("GuildID", data.GuildID)
	record.Set("ConnectionDetails", data.ConnectionDetails)

	// Save the record
	if err := currentApp.Save(record); err != nil { // Changed SaveRecord to Save
		log.Error("Error saving record", "collection", collectionNameOrId, "error", err)
		return err
	}
	log.Info("Server record saved successfully.", "collection", collectionNameOrId, "recordID", record.Id)
	return nil
}

// ListServersByUserIDAndGuildID retrieves a list of servers for a given UserID and GuildID
func ListServersByUserIDAndGuildID(userID string, guildID string) ([]*ServerInfo, error) {
	currentApp := GetApp()
	// Retrieve multiple records based on the UserID and GuildID filter
	records, err := currentApp.FindRecordsByFilter( // Removed .Dao()
		"servers", // collection name or ID
		"UserID = {:userID} && GuildID = {:guildID}", // filter with named placeholders
		"-created", // sort (descending by creation date)
		10,         // limit
		0,          // offset
		dbx.Params{"userID": userID, "guildID": guildID}, // parameters for the filter
	)
	if err != nil {
		log.Error("Error listing servers by UserID and GuildID", "userID", userID, "guildID", guildID, "error", err)
		return nil, err
	}

	log.Info("Number of records found for UserID in GuildID", "userID", userID, "guildID", guildID, "count", len(records))

	var servers []*ServerInfo
	for _, record := range records {
		server := &ServerInfo{
			UserID:            record.GetString("UserID"), // Using GetString for type safety
			GuildID:           record.GetString("GuildID"),
			ConnectionDetails: record.GetString("ConnectionDetails"),
		}
		servers = append(servers, server)
	}
	return servers, nil
}

// --- Reminder Functions ---

const remindersCollection = "reminders"

// CreateReminder saves a new reminder to PocketBase.
func CreateReminder(data *Reminder) error {
	currentApp := GetApp()
	collection, err := currentApp.FindCollectionByNameOrId(remindersCollection)
	if err != nil {
		log.Error("Error finding reminders collection", "error", err)
		return err
	}

	record := core.NewRecord(collection)
	record.Set("user_id", data.UserID)

	log.Infof("CreateReminder: target_user_ids = %v", data.TargetUserIDs)
	record.Set("target_user_ids", data.TargetUserIDs) // Set directly, no marshalling

	record.Set("message", data.Message)
	record.Set("channel_id", data.ChannelID)
	record.Set("guild_id", data.GuildID)
	record.Set("reminder_time", data.ReminderTime.UTC().Format(time.RFC3339Nano))

	// Set boolean value directly
	record.Set("is_recurring", data.IsRecurring)

	record.Set("recurrence_rule", data.RecurrenceRule)
	if !data.NextReminderTime.IsZero() {
		record.Set("next_reminder_time", data.NextReminderTime.UTC().Format(time.RFC3339Nano))
	}

	if err := currentApp.Save(record); err != nil {
		log.Error("Error saving reminder record", "error", err)
		return err
	}
	data.ID = record.Id // Set the ID from the created record
	log.Info("Reminder record saved successfully.", "recordID", record.Id)
	return nil
}

// GetDueReminders fetches reminders that are due to be triggered.
// This includes non-recurring reminders where reminder_time <= now
// and recurring reminders where next_reminder_time <= now.
func GetDueReminders() ([]*Reminder, error) {
	currentApp := GetApp()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	filter := "(is_recurring = false && reminder_time <= {:now}) || (is_recurring = true && next_reminder_time <= {:now})"
	params := dbx.Params{"now": now}

	records, err := currentApp.FindRecordsByFilter(
		remindersCollection,
		filter,
		"+reminder_time", // Sort by reminder_time to process earlier ones first
		50,               // Limit the number of reminders fetched at once
		0,
		params,
	)
	if err != nil {
		// A "no records found" result is not a real error in this context.
		if isNotFound(err) {
			return []*Reminder{}, nil
		}
		log.Error("Error fetching due reminders", "error", err)
		return nil, err
	}

	var reminders []*Reminder
	for _, record := range records {
		r := recordToReminder(record)
		reminders = append(reminders, r)
	}
	return reminders, nil
}

// UpdateReminder updates an existing reminder in PocketBase, typically for recurring reminders.
func UpdateReminder(data *Reminder) error {
	currentApp := GetApp()
	record, err := currentApp.FindRecordById(remindersCollection, data.ID)
	if err != nil {
		log.Error("Error finding reminder to update", "recordID", data.ID, "error", err)
		return err
	}

	record.Set("target_user_ids", data.TargetUserIDs)
	record.Set("message", data.Message)
	record.Set("reminder_time", data.ReminderTime.UTC().Format(time.RFC3339Nano)) // Original reminder time might change if edited
	record.Set("is_recurring", data.IsRecurring)                                  // Set boolean value directly
	record.Set("recurrence_rule", data.RecurrenceRule)

	if !data.NextReminderTime.IsZero() {
		record.Set("next_reminder_time", data.NextReminderTime.UTC().Format(time.RFC3339Nano))
	} else {
		record.Set("next_reminder_time", nil) // Clear it if zero
	}
	if !data.LastTriggeredAt.IsZero() {
		record.Set("last_triggered_at", data.LastTriggeredAt.UTC().Format(time.RFC3339Nano))
	} else {
		record.Set("last_triggered_at", nil) // Clear it if zero
	}

	if err := currentApp.Save(record); err != nil {
		log.Error("Error updating reminder record", "recordID", data.ID, "error", err)
		return err
	}
	log.Info("Reminder record updated successfully.", "recordID", data.ID)
	return nil
}

// DeleteReminder deletes a reminder from PocketBase.
func DeleteReminder(reminderID string) error {
	currentApp := GetApp()
	record, err := currentApp.FindRecordById(remindersCollection, reminderID)
	if err != nil {
		// If it's already deleted or not found, treat it as success for this operation's intent.
		if isNotFound(err) {
			log.Warn("Reminder not found, possibly already deleted.", "recordID", reminderID)
			return nil
		}
		log.Error("Error finding reminder to delete", "recordID", reminderID, "error", err)
		return err
	}

	if err := currentApp.Delete(record); err != nil { // Changed from DeleteRecord to Delete
		log.Error("Error deleting reminder record", "recordID", reminderID, "error", err)
		return err
	}
	log.Info("Reminder record deleted successfully.", "recordID", reminderID)
	return nil
}

// ListRemindersByUser fetches all active reminders for a given user.
func ListRemindersByUser(userID string) ([]*Reminder, error) {
	currentApp := GetApp()
	records, err := currentApp.FindRecordsByFilter(
		remindersCollection,
		"user_id = {:userID}",
		"", // No sort field since 'created' doesn't exist
		0,
		0,
		dbx.Params{"userID": userID},
	)
	if err != nil {
		if isNotFound(err) {
			return []*Reminder{}, nil
		}
		log.Error("Error listing reminders by user", "userID", userID, "error", err)
		return nil, err
	}

	var reminders []*Reminder
	for _, record := range records {
		r := recordToReminder(record)
		reminders = append(reminders, r)
	}
	return reminders, nil
}

// Helper function to convert a PocketBase record to a Reminder struct
func recordToReminder(record *core.Record) *Reminder {
	r := &Reminder{
		ID:             record.Id,
		UserID:         record.GetString("user_id"),
		Message:        record.GetString("message"),
		ChannelID:      record.GetString("channel_id"),
		GuildID:        record.GetString("guild_id"),
		IsRecurring:    record.GetBool("is_recurring"), // Use GetBool for boolean fields
		RecurrenceRule: record.GetString("recurrence_rule"),
	}

	// Debug log for raw target_user_ids value
	rawTargetUserIDs := record.Get("target_user_ids")
	log.Infof("recordToReminder: raw target_user_ids = %#v", rawTargetUserIDs)

	switch v := rawTargetUserIDs.(type) {
	case []interface{}:
		r.TargetUserIDs = make([]string, len(v))
		for i, val := range v {
			if idStr, ok := val.(string); ok {
				r.TargetUserIDs[i] = idStr
			}
		}
	case string:
		if v != "" {
			var ids []string
			if err := json.Unmarshal([]byte(v), &ids); err == nil {
				r.TargetUserIDs = ids
			} else {
				log.Warn("Failed to unmarshal target_user_ids string", "value", v, "error", err)
			}
		}
	case []byte:
		if len(v) > 0 {
			var ids []string
			if err := json.Unmarshal(v, &ids); err == nil {
				r.TargetUserIDs = ids
			} else {
				log.Warn("Failed to unmarshal target_user_ids []byte", "value", string(v), "error", err)
			}
		}
	default:
		// Handle any []byte-like type (e.g., types.JSONRaw) using reflection
		rv := reflect.ValueOf(v)
		if rv.Kind() == reflect.Slice && rv.Type().Elem().Kind() == reflect.Uint8 {
			b := make([]byte, rv.Len())
			reflect.Copy(reflect.ValueOf(b), rv)
			if len(b) > 0 {
				var ids []string
				if err := json.Unmarshal(b, &ids); err == nil {
					r.TargetUserIDs = ids
				} else {
					log.Warn("Failed to unmarshal target_user_ids []byte (reflect)", "value", string(b), "error", err)
				}
			}
		} else {
			log.Warn("Unknown type for target_user_ids", "type", fmt.Sprintf("%T", v))
		}
	}

	reminderTimeStr := record.GetString("reminder_time")
	if t, err := time.Parse(time.RFC3339Nano, reminderTimeStr); err == nil {
		r.ReminderTime = t.In(reminderLocation)
	} else {
		log.Warn("Failed to parse reminder_time", "value", reminderTimeStr, "error", err)
	}

	nextReminderTimeStr := record.GetString("next_reminder_time")
	if t, err := time.Parse(time.RFC3339Nano, nextReminderTimeStr); err == nil && !t.IsZero() {
		r.NextReminderTime = t.In(reminderLocation)
	} else if err != nil && nextReminderTimeStr != "" { // Only log if there was a value but parsing failed
		log.Warn("Failed to parse next_reminder_time", "value", nextReminderTimeStr, "error", err)
	}

	lastTriggeredAtStr := record.GetString("last_triggered_at")
	if t, err := time.Parse(time.RFC3339Nano, lastTriggeredAtStr); err == nil && !t.IsZero() {
		r.LastTriggeredAt = t.In(reminderLocation)
	} else if err != nil && lastTriggeredAtStr != "" { // Only log if there was a value but parsing failed
		log.Warn("Failed to parse last_triggered_at", "value", lastTriggeredAtStr, "error", err)
	}

	// PocketBase managed fields
	r.CreatedAt, _ = time.Parse(time.RFC3339Nano, record.GetString("created"))
	r.UpdatedAt, _ = time.Parse(time.RFC3339Nano, record.GetString("updated"))

	return r
}

// GetReminderByID fetches a single reminder by its PocketBase ID.
func GetReminderByID(reminderID string) (*Reminder, error) {
	currentApp := GetApp()
	record, err := currentApp.FindRecordById(remindersCollection, reminderID)
	if err != nil {
		// Don't log error here if it's just "not found", as that's a valid case for checks.
		// The caller can decide to log based on context.
		return nil, err
	}
	return recordToReminder(record), nil
}
