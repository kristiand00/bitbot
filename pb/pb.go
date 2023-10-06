// pb.go
package pb

import (
	"sync"

	"github.com/charmbracelet/log"
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

func CreateRecord(collectionName string, record *ServerInfo) error {
	// Find the collection
	collection, err := app.Dao().FindCollectionByNameOrId(collectionName)
	if err != nil {
		log.Error(err)
		return err
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

	return nil
}
