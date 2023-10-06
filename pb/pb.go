// pb.go
package pb

import (
	"sync"

	"github.com/charmbracelet/log"
	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/models"
)

var (
	app  *pocketbase.PocketBase
	once sync.Once
)

type ServerInfo struct {
	UserID            string
	ConnectionDetails string
}

// Init initializes the PocketBase app
func Init() {
	once.Do(func() {
		app = pocketbase.New()

		// Add any necessary configuration or initialization here

		func() {
			if err := app.Start(); err != nil {
				log.Fatal(err)
			}
		}()
	})
}

func GetRecordById(collectionID string, recordID string) (*models.Record, error) {
	// Access the PocketBase DAO and perform database operations
	// Example: Retrieve all records from the "articles" collection
	record, err := app.Dao().FindRecordById(collectionID, recordID)
	if err != nil {
		log.Error(err)
		return nil, err
	}

	return record, nil
}

// CreateRecord creates a new record in the specified collection
func CreateRecord(collectionName string, record *ServerInfo) error {
	// Find the collection
	collection, err := app.Dao().FindCollectionByNameOrId(collectionName)
	if err != nil {
		log.Error(err)
		return err
	}

	// Check if the server already exists
	existingRecord, err := app.Dao().FindFirstRecordByFilter(
		collectionName,
		"UserID = {:userID} && ConnectionDetails = {:connectionDetails}",
		dbx.Params{"userID": record.UserID, "connectionDetails": record.ConnectionDetails},
	)
	if err != nil {
		log.Error(err)
		return err
	}

	// If the record already exists, return without saving
	if existingRecord != nil {
		log.Info("Server already exists.")
		return nil
	}

	// Create a new record
	newRecord := models.NewRecord(collection)

	// Convert ServerInfo to a map
	recordMap := map[string]interface{}{
		"UserID":            record.UserID,
		"ConnectionDetails": record.ConnectionDetails,
	}

	// Bulk load with record.Load(map[string]interface{})
	newRecord.Load(recordMap)

	// Save the record
	if err := app.Dao().SaveRecord(newRecord); err != nil {
		log.Error(err)
		return err
	}

	log.Info("Server saved successfully.")
	return nil
}

// ListServersByUserID retrieves a list of servers for a given UserID
func ListServersByUserID(userID string) ([]*ServerInfo, error) {

	// Retrieve multiple records based on the UserID filter
	records, err := app.Dao().FindRecordsByFilter(
		"servers",                    // collection
		"UserID = {:userID}",         // filter
		"-created",                   // sort (empty for no sorting)
		10,                           // limit (0 for no limit)
		0,                            // offset
		dbx.Params{"userID": userID}, // parameter for the placeholder
	)
	if err != nil {
		log.Error(err)
		return nil, err
	}

	// Log the number of records retrieved
	log.Info("Number of records:", len(records))

	// Convert records to []*ServerInfo
	var servers []*ServerInfo
	for _, record := range records {
		// Log details of each record
		log.Info("Record details:", record)

		// Print each field to check if they are set
		log.Info("UserID:", record.Get("UserID"))
		log.Info("ConnectionDetails:", record.Get("ConnectionDetails"))

		// Convert record to ServerInfo
		server := &ServerInfo{
			UserID:            record.Get("UserID").(string),
			ConnectionDetails: record.Get("ConnectionDetails").(string),
			// Add other fields as needed
		}
		servers = append(servers, server)
	}

	return servers, nil
}
