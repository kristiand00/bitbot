// Package migrations holds the ordered set of PocketBase schema/data migrations
// for the bot. Each migration carries its own idempotent "is it needed?" check,
// so migrations are safe to run on every startup: only the ones whose target
// state is not yet present actually apply.
//
// To add a schema change, append a new Migration to the list. Never edit or
// reorder existing entries — add a new one that transforms the current state.
package migrations

import (
	"fmt"

	"github.com/charmbracelet/log"
	"github.com/pocketbase/pocketbase/core"
)

// Collection names (kept as literals here to keep this package free of any
// dependency on the parent pb package).
const (
	remindersCollection  = "reminders"
	serversCollection    = "servers"
	mcpServersCollection = "mcp_servers"
)

// Migration is a single schema/data change.
type Migration struct {
	Name string
	// Optional migrations that fail are logged and skipped rather than aborting
	// startup (used for non-critical features like MCP integration).
	Optional bool
	// Needed reports whether the migration still has work to do.
	Needed func(core.App) (bool, error)
	// Apply performs the change.
	Apply func(core.App) error
}

// list is the ordered set of migrations. Append-only.
var list = []Migration{
	{
		Name:   "create_reminders_collection",
		Needed: collectionMissing(remindersCollection),
		Apply:  createRemindersCollection,
	},
	{
		Name:   "create_servers_collection",
		Needed: collectionMissing(serversCollection),
		Apply:  createServersCollection,
	},
	{
		Name:     "create_mcp_servers_collection",
		Optional: true,
		Needed:   collectionMissing(mcpServersCollection),
		Apply:    createMCPServersCollection,
	},
	{
		Name:     "mcp_add_public_field",
		Optional: true,
		Needed:   fieldMissing(mcpServersCollection, "public"),
		Apply:    addBoolField(mcpServersCollection, "public"),
	},
	{
		Name:     "mcp_add_owner_field",
		Optional: true,
		Needed:   fieldMissing(mcpServersCollection, "owner"),
		Apply:    addTextField(mcpServersCollection, "owner"),
	},
	{
		Name:     "mcp_add_visibility_field",
		Optional: true,
		Needed:   fieldMissing(mcpServersCollection, "visibility"),
		Apply:    addTextField(mcpServersCollection, "visibility"),
	},
	{
		Name:     "mcp_backfill_visibility",
		Optional: true,
		Needed:   mcpVisibilityBackfillNeeded,
		Apply:    backfillMCPVisibility,
	},
}

// Run applies every migration whose Needed check reports work to do, in order.
// A failing required migration aborts (returns an error); a failing optional one
// is logged and skipped.
func Run(app core.App) error {
	for _, m := range list {
		need, err := m.Needed(app)
		if err != nil {
			if m.Optional {
				log.Warnf("migration %q: needed-check failed, skipping: %v", m.Name, err)
				continue
			}
			return fmt.Errorf("migration %q needed-check: %w", m.Name, err)
		}
		if !need {
			continue
		}
		log.Infof("applying migration: %s", m.Name)
		if err := m.Apply(app); err != nil {
			if m.Optional {
				log.Warnf("migration %q failed, skipping: %v", m.Name, err)
				continue
			}
			return fmt.Errorf("migration %q: %w", m.Name, err)
		}
	}
	return nil
}

// --- Needed-check helpers ---

func collectionMissing(name string) func(core.App) (bool, error) {
	return func(app core.App) (bool, error) {
		_, err := app.FindCollectionByNameOrId(name)
		return err != nil, nil
	}
}

func fieldMissing(collection, field string) func(core.App) (bool, error) {
	return func(app core.App) (bool, error) {
		c, err := app.FindCollectionByNameOrId(collection)
		if err != nil {
			return false, nil // collection missing: its create migration handles it
		}
		return c.Fields.GetByName(field) == nil, nil
	}
}

// --- Field-adding helpers ---
//
// SaveNoValidate is used for field additions because these collections were
// created with id == name, which PocketBase's collection-update validation
// rejects (a name must not match an existing collection id) even though the
// schema change itself is valid.

func addTextField(collection, field string) func(core.App) error {
	return func(app core.App) error {
		c, err := app.FindCollectionByNameOrId(collection)
		if err != nil {
			return err
		}
		c.Fields.Add(&core.TextField{Name: field, Required: false})
		return app.SaveNoValidate(c)
	}
}

func addBoolField(collection, field string) func(core.App) error {
	return func(app core.App) error {
		c, err := app.FindCollectionByNameOrId(collection)
		if err != nil {
			return err
		}
		c.Fields.Add(&core.BoolField{Name: field, Required: false})
		return app.SaveNoValidate(c)
	}
}

// --- Collection creators ---

func createRemindersCollection(app core.App) error {
	c := core.NewBaseCollection(remindersCollection, remindersCollection)
	c.Fields.Add(&core.TextField{Name: "user_id", Required: true})
	c.Fields.Add(&core.JSONField{Name: "target_user_ids", Required: true})
	c.Fields.Add(&core.TextField{Name: "message", Required: true})
	c.Fields.Add(&core.TextField{Name: "channel_id", Required: true})
	c.Fields.Add(&core.TextField{Name: "guild_id", Required: false})
	c.Fields.Add(&core.TextField{Name: "reminder_time", Required: true})
	c.Fields.Add(&core.BoolField{Name: "is_recurring", Required: false})
	c.Fields.Add(&core.TextField{Name: "recurrence_rule", Required: false})
	c.Fields.Add(&core.TextField{Name: "next_reminder_time", Required: false})
	c.Fields.Add(&core.TextField{Name: "last_triggered_at", Required: false})
	return app.Save(c)
}

func createServersCollection(app core.App) error {
	c := core.NewBaseCollection(serversCollection, serversCollection)
	c.Fields.Add(&core.TextField{Name: "UserID", Required: true})
	c.Fields.Add(&core.TextField{Name: "GuildID", Required: true})
	c.Fields.Add(&core.TextField{Name: "ConnectionDetails", Required: true})
	return app.Save(c)
}

// createMCPServersCollection creates the collection with its original fields;
// later migrations add public/owner/visibility. Creating with the original
// shape keeps the migration history a clean sequence of deltas.
func createMCPServersCollection(app core.App) error {
	c := core.NewBaseCollection(mcpServersCollection, mcpServersCollection)
	c.Fields.Add(&core.TextField{Name: "name", Required: true})
	c.Fields.Add(&core.TextField{Name: "url", Required: true})
	c.Fields.Add(&core.TextField{Name: "token", Required: false})
	c.Fields.Add(&core.BoolField{Name: "enabled", Required: false})
	return app.Save(c)
}

// --- Data migrations ---

func mcpVisibilityBackfillNeeded(app core.App) (bool, error) {
	c, err := app.FindCollectionByNameOrId(mcpServersCollection)
	if err != nil || c.Fields.GetByName("visibility") == nil {
		return false, nil
	}
	records, err := app.FindAllRecords(mcpServersCollection)
	if err != nil {
		return false, nil
	}
	for _, r := range records {
		if r.GetString("visibility") == "" {
			return true, nil
		}
	}
	return false, nil
}

// backfillMCPVisibility sets visibility for rows created before the field
// existed, derived from the legacy "public" boolean (public => everyone, else
// admins).
func backfillMCPVisibility(app core.App) error {
	records, err := app.FindAllRecords(mcpServersCollection)
	if err != nil {
		return nil
	}
	for _, r := range records {
		if r.GetString("visibility") != "" {
			continue
		}
		vis := "admins"
		if r.GetBool("public") {
			vis = "public"
		}
		r.Set("visibility", vis)
		if err := app.Save(r); err != nil {
			log.Warnf("backfill visibility for %s: %v", r.Id, err)
		}
	}
	return nil
}
