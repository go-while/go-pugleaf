package database

import (
	"fmt"

	"github.com/go-while/go-pugleaf/internal/models"
)

const query_upsertSectionInsert = `INSERT OR IGNORE INTO sections (name, display_name, description, show_in_header, enable_local_spool, sort_order)
	 VALUES (?, ?, ?, ?, ?, ?)`

const query_upsertSectionSelectID = `SELECT id FROM sections WHERE name = ?`

// UpsertSectionID inserts the section unless a row with the same name exists and returns
// the id of the stored row. Importers must not read LastInsertId after an INSERT OR
// IGNORE: when the section already exists nothing is inserted and LastInsertId still
// holds the id of an unrelated earlier statement, so the section_groups rows that follow
// would fail against the foreign key or attach the groups to the wrong section.
func (db *Database) UpsertSectionID(section *models.Section) (int64, error) {
	if section == nil {
		return 0, fmt.Errorf("UpsertSectionID: section is nil")
	}
	if _, err := RetryableExec(db.mainDB, query_upsertSectionInsert,
		section.Name, section.DisplayName, section.Description,
		section.ShowInHeader, section.EnableLocalSpool, section.SortOrder); err != nil {
		return 0, fmt.Errorf("UpsertSectionID: insert section '%s': %w", section.Name, err)
	}
	var id int64
	if err := RetryableQueryRowScan(db.mainDB, query_upsertSectionSelectID, []interface{}{section.Name}, &id); err != nil {
		return 0, fmt.Errorf("UpsertSectionID: read id of section '%s': %w", section.Name, err)
	}
	return id, nil
}
