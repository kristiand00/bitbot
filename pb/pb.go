package pb

import (
	"sync" // fmt was removed as it was unused in the last good version

	"github.com/charmbracelet/log"
	"github.com/pocketbase/dbx"
	"encoding/json" // For unmarshalling target_user_ids fallback
	"strings"       // For error checking in DeleteReminder
	"time"          // Added for reminder timestamps

	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/core" // Changed from models
)

var (
	appOnce sync.Once
	pbApp   *pocketbase.PocketBase // Renamed from 'app' to avoid conflict if any package level 'app' existed
)

type ServerInfo struct {
	UserID            string
	ConnectionDetails string
}

// Reminder struct corresponds to the 'reminders' collection schema
type Reminder struct {
	ID                string    `db:"id" json:"id"` // PocketBase record ID
	UserID            string    `db:"user_id" json:"user_id"`
	TargetUserIDs     []string  `db:"target_user_ids" json:"target_user_ids"` // Stored as JSON in PB
	Message           string    `db:"message" json:"message"`
	ChannelID         string    `db:"channel_id" json:"channel_id"`
	GuildID           string    `db:"guild_id" json:"guild_id,omitempty"`
	ReminderTime      time.Time `db:"reminder_time" json:"reminder_time"`           // Specific time for the reminder
	IsRecurring       bool      `db:"is_recurring" json:"is_recurring"`           // Is it a recurring reminder?
	RecurrenceRule    string    `db:"recurrence_rule" json:"recurrence_rule,omitempty"` // e.g., "daily", "weekly"
	NextReminderTime  time.Time `db:"next_reminder_time" json:"next_reminder_time,omitempty"` // Next time for recurring
	LastTriggeredAt   time.Time `db:"last_triggered_at" json:"last_triggered_at,omitempty"` // Last time it was triggered
	CreatedAt         time.Time `db:"created" json:"created"`                   // PocketBase managed
	UpdatedAt         time.Time `db:"updated" json:"updated"`                   // PocketBase managed
}

// Init initializes the PocketBase app
func Init() {
	appOnce.Do(func() {
		pbApp = pocketbase.New()

		// Bootstrap the app (important for running as a library)
		if err := pbApp.Bootstrap(); err != nil {
			log.Fatalf("Failed to bootstrap PocketBase: %v", err)
		}
		// Note: pbApp.Start() is not called here as we are using PocketBase as a library,
		// not running its full HTTP server. Bootstrap prepares it for DAO operations.
		log.Info("PocketBase initialized and bootstrapped.")
	})
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
		"UserID = {:userID} && ConnectionDetails = {:connectionDetails}", // Using named placeholders
		dbx.Params{"userID": data.UserID, "connectionDetails": data.ConnectionDetails},
	)
	// Not checking error for FindFirstRecordByFilter here, as a "not found" is not an error for this logic.
	// A nil existingRecord means it's safe to create.

	if existingRecord != nil {
		log.Info("Server record already exists.", "userID", data.UserID, "details", data.ConnectionDetails)
		return nil // Not an error, just already exists
	}

	// Create a new record
	record := core.NewRecord(collection) // Use core.NewRecord

	// Set fields using Set() method for type safety and internal handling
	record.Set("UserID", data.UserID)
	record.Set("ConnectionDetails", data.ConnectionDetails)

	// Save the record
	if err := currentApp.Save(record); err != nil { // Changed SaveRecord to Save
		log.Error("Error saving record", "collection", collectionNameOrId, "error", err)
		return err
	}
	log.Info("Server record saved successfully.", "collection", collectionNameOrId, "recordID", record.Id)
	return nil
}

// ListServersByUserID retrieves a list of servers for a given UserID
func ListServersByUserID(userID string) ([]*ServerInfo, error) {
	currentApp := GetApp()
	// Retrieve multiple records based on the UserID filter
	records, err := currentApp.FindRecordsByFilter( // Removed .Dao()
		"servers",                    // collection name or ID
		"UserID = {:userID}",         // filter with named placeholder
		"-created",                   // sort (descending by creation date)
		10,                           // limit
		0,                            // offset
		dbx.Params{"userID": userID}, // parameters for the filter
	)
	if err != nil {
		log.Error("Error listing servers by UserID", "userID", userID, "error", err)
		return nil, err
	}

	log.Info("Number of records found for UserID", "userID", userID, "count", len(records))

	var servers []*ServerInfo
	for _, record := range records {
		server := &ServerInfo{
			UserID:            record.GetString("UserID"), // Using GetString for type safety
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
	record.Set("target_user_ids", data.TargetUserIDs) // PocketBase handles JSON marshalling for 'json' type fields
	record.Set("message", data.Message)
	record.Set("channel_id", data.ChannelID)
	record.Set("guild_id", data.GuildID)
	record.Set("reminder_time", data.ReminderTime.UTC().Format(time.RFC3339Nano))
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

	// Query for non-recurring reminders
	filterNonRecurring := dbx.NewExp("is_recurring = false AND reminder_time <= {:now}", dbx.Params{"now": now})
	// Query for recurring reminders
	filterRecurring := dbx.NewExp("is_recurring = true AND next_reminder_time <= {:now}", dbx.Params{"now": now})

	// Combine filters with OR
	combinedFilter := dbx.Or(filterNonRecurring, filterRecurring)

	records, err := currentApp.FindRecordsByFilter(
		remindersCollection,
		combinedFilter.Build(), // Build the expression to get the string
		"+reminder_time",       // Sort by reminder_time to process earlier ones first
		50,                     // Limit the number of reminders fetched at once
		0,
		combinedFilter.Params(), // Pass the parameters
	)
	if err != nil {
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
	record.Set("is_recurring", data.IsRecurring)
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
		log.Error("Error finding reminder to delete", "recordID", reminderID, "error", err)
		// If it's already deleted or not found, we can consider it a success for this operation's intent.
		if strings.Contains(err.Error(), "Failed to find record") { // TODO: check for specific error type if available
			log.Warn("Reminder not found, possibly already deleted.", "recordID", reminderID)
			return nil
		}
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
		"+reminder_time", // Sort by next due time
		0,                // No limit, get all
		0,
		dbx.Params{"userID": userID},
	)
	if err != nil {
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
		IsRecurring:    record.GetBool("is_recurring"),
		RecurrenceRule: record.GetString("recurrence_rule"),
	}

	// PocketBase stores JSON array as string internally, Get() returns it as such.
	// We need to unmarshal it into []string.
	// However, the `json` field type in PocketBase should automatically handle this
	// if the struct field is `[]string`. Let's try direct Get() first.
	// If Get("target_user_ids") returns a string, we'll need json.Unmarshal.
	// For now, assume PocketBase's Go driver handles this for `Get()` on json fields.
	// Update: Record.Get() on a json field that is an array of strings might return []interface{}.
	// We need to convert it.
	rawTargetUserIDs := record.Get("target_user_ids")
	if targetIDs, ok := rawTargetUserIDs.([]interface{}); ok {
		r.TargetUserIDs = make([]string, len(targetIDs))
		for i, v := range targetIDs {
			if idStr, okStr := v.(string); okStr {
				r.TargetUserIDs[i] = idStr
			}
		}
	} else if targetIDsStr, okStr := rawTargetUserIDs.(string); okStr && targetIDsStr != "" {
		// Fallback if it's a JSON string (less ideal from Get)
		var ids []string
		if err := json.Unmarshal([]byte(targetIDsStr), &ids); err == nil {
			r.TargetUserIDs = ids
		} else {
			log.Warn("Failed to unmarshal target_user_ids string", "value", targetIDsStr, "error", err)
		}
	}


	reminderTimeStr := record.GetString("reminder_time")
	if t, err := time.Parse(time.RFC3339Nano, reminderTimeStr); err == nil {
		r.ReminderTime = t
	} else {
		log.Warn("Failed to parse reminder_time", "value", reminderTimeStr, "error", err)
	}

	nextReminderTimeStr := record.GetString("next_reminder_time")
	if t, err := time.Parse(time.RFC3339Nano, nextReminderTimeStr); err == nil && !t.IsZero() {
		r.NextReminderTime = t
	} else if err != nil && nextReminderTimeStr != "" { // Only log if there was a value but parsing failed
		log.Warn("Failed to parse next_reminder_time", "value", nextReminderTimeStr, "error", err)
	}


	lastTriggeredAtStr := record.GetString("last_triggered_at")
	if t, err := time.Parse(time.RFC3339Nano, lastTriggeredAtStr); err == nil && !t.IsZero() {
		r.LastTriggeredAt = t
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
