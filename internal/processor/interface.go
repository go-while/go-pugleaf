package processor

import (
	"github.com/go-while/go-pugleaf/internal/database"
)

/*
// MsgIdExists implements the ProcessorInterface interface
// Returns true if the message ID exists in the cache for the given group
func (proc *Processor) MsgIdExists(group *string, messageID string) bool {
	item := proc.MsgIdCache.MsgIdExists(group, messageID)
	return item != nil
}
*/

func (proc *Processor) IsNewsGroupInSectionsDB(name *string) bool {
	return proc.DB.IsNewsGroupInSections(*name)
}

// ForceCloseGroupDB implements the ProcessorInterface interface
// Forces closure of group database connections
func (proc *Processor) ForceCloseGroupDB(groupsDB *database.GroupDB) error {
	return proc.DB.ForceCloseGroupDB(groupsDB)
}
