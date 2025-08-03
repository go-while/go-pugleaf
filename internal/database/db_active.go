package database

import (
	"fmt"

	"github.com/go-while/go-pugleaf/internal/models"
)

// initActiveDB initializes the active database for newsgroup registry
func (db *Database) initActiveDB() error {
	activeDB, err := NewActiveDB(db.dbconfig.DataDir)
	if err != nil {
		return fmt.Errorf("failed to create active database: %w", err)
	}

	db.activeDB = activeDB
	return nil
}

// GetActiveDB returns the active database for newsgroup registry operations
func (db *Database) GetActiveDB() *ActiveDB {
	return db.activeDB
}

// ActiveDB wrapper methods for convenience

// AddActiveNewsgroup adds a new newsgroup to the active database
func (db *Database) AddActiveNewsgroup(groupName, description string) (*models.ActiveNewsgroup, error) {
	return db.activeDB.AddNewsgroup(groupName, description)
}

// GetActiveNewsgroup gets an active newsgroup by name
func (db *Database) GetActiveNewsgroup(groupName string) (*models.ActiveNewsgroup, error) {
	return db.activeDB.GetNewsgroup(groupName)
}

// GetActiveNewsgroupByID gets an active newsgroup by ID
func (db *Database) GetActiveNewsgroupByID(groupID int) (*models.ActiveNewsgroup, error) {
	return db.activeDB.GetNewsgroupByID(groupID)
}

// ListActiveNewsgroups lists all active newsgroups
func (db *Database) ListActiveNewsgroups() ([]*models.ActiveNewsgroup, error) {
	return db.activeDB.ListNewsgroups()
}

// UpdateActiveWatermarks updates high/low water marks for an active newsgroup
func (db *Database) UpdateActiveWatermarks(groupID int, highWater, lowWater int) error {
	return db.activeDB.UpdateWatermarks(groupID, highWater, lowWater)
}

// UpdateActiveMessageCount updates the message count for an active newsgroup
func (db *Database) UpdateActiveMessageCount(groupID int, messageCount int) error {
	return db.activeDB.UpdateMessageCount(groupID, messageCount)
}

// SetActiveNewsgroupStatus sets the status of an active newsgroup
func (db *Database) SetActiveNewsgroupStatus(groupID int, status string) error {
	return db.activeDB.SetStatus(groupID, status)
}

// RemoveActiveNewsgroup removes a newsgroup from the active database
func (db *Database) RemoveActiveNewsgroup(groupID int) error {
	return db.activeDB.RemoveNewsgroup(groupID)
}
