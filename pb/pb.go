package pb

import (
	"sync" // fmt was removed as it was unused in the last good version

	"github.com/charmbracelet/log"
	"github.com/pocketbase/dbx"
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
