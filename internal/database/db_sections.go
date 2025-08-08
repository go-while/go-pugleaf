package database

import (
	"database/sql"
	"fmt"

	"github.com/go-while/go-pugleaf/internal/models"
)

// GetAllSections retrieves all sections ordered by sort_order
func (db *Database) GetAllSections() ([]*models.Section, error) {
	query := `
		SELECT id, name, display_name, description, show_in_header, enable_local_spool, sort_order, created_at
		FROM sections
		ORDER BY sort_order ASC, display_name ASC
	`

	rows, err := retryableQuery(db.mainDB, query)
	if err != nil {
		return nil, fmt.Errorf("failed to query sections: %w", err)
	}
	defer rows.Close()

	var sections []*models.Section
	for rows.Next() {
		section := &models.Section{}
		err := rows.Scan(
			&section.ID,
			&section.Name,
			&section.DisplayName,
			&section.Description,
			&section.ShowInHeader,
			&section.EnableLocalSpool,
			&section.SortOrder,
			&section.CreatedAt,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan section: %w", err)
		}
		sections = append(sections, section)
	}

	return sections, rows.Err()
}

// GetAllSectionsWithCounts retrieves all sections with their newsgroup counts
func (db *Database) GetAllSectionsWithCounts() ([]*models.Section, error) {
	query := `
		SELECT
			s.id, s.name, s.display_name, s.description, s.show_in_header,
			s.enable_local_spool, s.sort_order, s.created_at,
			COALESCE(COUNT(sg.id), 0) as group_count
		FROM sections s
		LEFT JOIN section_groups sg ON s.id = sg.section_id
		GROUP BY s.id, s.name, s.display_name, s.description, s.show_in_header,
		         s.enable_local_spool, s.sort_order, s.created_at
		ORDER BY s.sort_order ASC, s.display_name ASC
	`

	rows, err := retryableQuery(db.mainDB, query)
	if err != nil {
		return nil, fmt.Errorf("failed to query sections with counts: %w", err)
	}
	defer rows.Close()

	var sections []*models.Section
	for rows.Next() {
		section := &models.Section{}
		err := rows.Scan(
			&section.ID,
			&section.Name,
			&section.DisplayName,
			&section.Description,
			&section.ShowInHeader,
			&section.EnableLocalSpool,
			&section.SortOrder,
			&section.CreatedAt,
			&section.GroupCount,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan section with count: %w", err)
		}
		sections = append(sections, section)
	}

	return sections, rows.Err()
}

// GetAllSectionGroups retrieves all section group assignments
func (db *Database) GetAllSectionGroups() ([]*models.SectionGroup, error) {
	query := `
		SELECT id, section_id, newsgroup_name, group_description, sort_order, is_category_header, created_at
		FROM section_groups
		ORDER BY section_id ASC, sort_order ASC, newsgroup_name ASC
	`

	rows, err := retryableQuery(db.mainDB, query)
	if err != nil {
		return nil, fmt.Errorf("failed to query section groups: %w", err)
	}
	defer rows.Close()

	var sectionGroups []*models.SectionGroup
	for rows.Next() {
		sg := &models.SectionGroup{}
		err := rows.Scan(
			&sg.ID,
			&sg.SectionID,
			&sg.NewsgroupName,
			&sg.GroupDescription,
			&sg.SortOrder,
			&sg.IsCategoryHeader,
			&sg.CreatedAt,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan section group: %w", err)
		}
		sectionGroups = append(sectionGroups, sg)
	}

	return sectionGroups, rows.Err()
}

// GetSectionByID retrieves a section by its ID
func (db *Database) GetSectionByID(id int) (*models.Section, error) {
	query := `
		SELECT id, name, display_name, description, show_in_header, enable_local_spool, sort_order, created_at
		FROM sections
		WHERE id = ?
	`

	section := &models.Section{}
	err := retryableQueryRowScan(db.mainDB, query, []interface{}{id},
		&section.ID,
		&section.Name,
		&section.DisplayName,
		&section.Description,
		&section.ShowInHeader,
		&section.EnableLocalSpool,
		&section.SortOrder,
		&section.CreatedAt,
	)

	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("section not found")
		}
		return nil, fmt.Errorf("failed to get section: %w", err)
	}

	return section, nil
}

// SectionNameExists checks if a section name already exists
func (db *Database) SectionNameExists(name string) (bool, error) {
	query := `SELECT COUNT(*) FROM sections WHERE name = ?`

	var count int
	err := retryableQueryRowScan(db.mainDB, query, []interface{}{name}, &count)
	if err != nil {
		return false, fmt.Errorf("failed to check section name existence: %w", err)
	}

	return count > 0, nil
}

// SectionNameExistsExcluding checks if a section name exists excluding a specific ID
func (db *Database) SectionNameExistsExcluding(name string, excludeID int) (bool, error) {
	query := `SELECT COUNT(*) FROM sections WHERE name = ? AND id != ?`

	var count int
	err := retryableQueryRowScan(db.mainDB, query, []interface{}{name, excludeID}, &count)
	if err != nil {
		return false, fmt.Errorf("failed to check section name existence: %w", err)
	}

	return count > 0, nil
}

// CreateSection creates a new section
func (db *Database) CreateSection(section *models.Section) error {
	query := `
		INSERT INTO sections (name, display_name, description, show_in_header, enable_local_spool, sort_order, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`

	result, err := retryableExec(db.mainDB, query,
		section.Name,
		section.DisplayName,
		section.Description,
		section.ShowInHeader,
		section.EnableLocalSpool,
		section.SortOrder,
		section.CreatedAt,
	)

	if err != nil {
		return fmt.Errorf("failed to create section: %w", err)
	}

	id, err := result.LastInsertId()
	if err != nil {
		return fmt.Errorf("failed to get section ID: %w", err)
	}

	section.ID = int(id)
	return nil
}

// UpdateSection updates an existing section
func (db *Database) UpdateSection(section *models.Section) error {
	query := `
		UPDATE sections
		SET name = ?, display_name = ?, description = ?, show_in_header = ?, enable_local_spool = ?, sort_order = ?
		WHERE id = ?
	`

	result, err := retryableExec(db.mainDB, query,
		section.Name,
		section.DisplayName,
		section.Description,
		section.ShowInHeader,
		section.EnableLocalSpool,
		section.SortOrder,
		section.ID,
	)

	if err != nil {
		return fmt.Errorf("failed to update section: %w", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to get rows affected: %w", err)
	}

	if rowsAffected == 0 {
		return fmt.Errorf("section not found")
	}

	return nil
}

// DeleteSection deletes a section and all its group assignments
func (db *Database) DeleteSection(id int) error {
	return retryableTransactionExec(db.mainDB, func(tx *sql.Tx) error {
		// Delete section groups first (foreign key constraint)
		_, err := tx.Exec("DELETE FROM section_groups WHERE section_id = ?", id)
		if err != nil {
			return fmt.Errorf("failed to delete section groups: %w", err)
		}

		// Delete the section
		result, err := tx.Exec("DELETE FROM sections WHERE id = ?", id)
		if err != nil {
			return fmt.Errorf("failed to delete section: %w", err)
		}

		rowsAffected, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("failed to get rows affected: %w", err)
		}

		if rowsAffected == 0 {
			return fmt.Errorf("section not found")
		}

		return nil
	})
}

// GetSectionGroupByID retrieves a section group by its ID
func (db *Database) GetSectionGroupByID(id int) (*models.SectionGroup, error) {
	query := `
		SELECT id, section_id, newsgroup_name, group_description, sort_order, is_category_header, created_at
		FROM section_groups
		WHERE id = ?
	`

	sg := &models.SectionGroup{}
	err := retryableQueryRowScan(db.mainDB, query, []interface{}{id},
		&sg.ID,
		&sg.SectionID,
		&sg.NewsgroupName,
		&sg.GroupDescription,
		&sg.SortOrder,
		&sg.IsCategoryHeader,
		&sg.CreatedAt,
	)

	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("section group not found")
		}
		return nil, fmt.Errorf("failed to get section group: %w", err)
	}

	return sg, nil
}

// SectionGroupExists checks if a newsgroup is already assigned to a section
func (db *Database) SectionGroupExists(sectionID int, newsgroupName string) (bool, error) {
	query := `SELECT COUNT(*) FROM section_groups WHERE section_id = ? AND newsgroup_name = ?`

	var count int
	err := retryableQueryRowScan(db.mainDB, query, []interface{}{sectionID, newsgroupName}, &count)
	if err != nil {
		return false, fmt.Errorf("failed to check section group existence: %w", err)
	}

	return count > 0, nil
}

// CreateSectionGroup creates a new section group assignment
func (db *Database) CreateSectionGroup(sg *models.SectionGroup) error {
	query := `
		INSERT INTO section_groups (section_id, newsgroup_name, group_description, sort_order, is_category_header, created_at)
		VALUES (?, ?, ?, ?, ?, ?)
	`

	result, err := retryableExec(db.mainDB, query,
		sg.SectionID,
		sg.NewsgroupName,
		sg.GroupDescription,
		sg.SortOrder,
		sg.IsCategoryHeader,
		sg.CreatedAt,
	)

	if err != nil {
		return fmt.Errorf("failed to create section group: %w", err)
	}

	id, err := result.LastInsertId()
	if err != nil {
		return fmt.Errorf("failed to get section group ID: %w", err)
	}

	sg.ID = int(id)
	return nil
}

// DeleteSectionGroup deletes a section group assignment
func (db *Database) DeleteSectionGroup(id int) error {
	query := `DELETE FROM section_groups WHERE id = ?`

	result, err := retryableExec(db.mainDB, query, id)
	if err != nil {
		return fmt.Errorf("failed to delete section group: %w", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to get rows affected: %w", err)
	}

	if rowsAffected == 0 {
		return fmt.Errorf("section group not found")
	}

	return nil
}

// GetNewsgroupByName retrieves a newsgroup by name (for getting description)
func (db *Database) GetNewsgroupByName(name string) (*models.Newsgroup, error) {
	query := `
		SELECT id, name, active, description, last_article, message_count, expiry_days, max_articles, max_art_size,
		       high_water, low_water, status, created_at, updated_at
		FROM newsgroups
		WHERE name = ?
	`

	ng := &models.Newsgroup{}
	err := retryableQueryRowScan(db.mainDB, query, []interface{}{name},
		&ng.ID,
		&ng.Name,
		&ng.Active,
		&ng.Description,
		&ng.LastArticle,
		&ng.MessageCount,
		&ng.ExpiryDays,
		&ng.MaxArticles,
		&ng.MaxArtSize,
		&ng.HighWater,
		&ng.LowWater,
		&ng.Status,
		&ng.CreatedAt,
		&ng.UpdatedAt,
	)

	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("newsgroup not found")
		}
		return nil, fmt.Errorf("failed to get newsgroup: %w", err)
	}

	return ng, nil
}
