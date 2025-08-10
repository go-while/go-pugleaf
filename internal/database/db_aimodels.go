package database

import (
	"database/sql"
	"log"
	"time"

	"github.com/go-while/go-pugleaf/internal/models"
)

// GetActiveAIModels returns all active AI models ordered by sort_order
func (db *Database) GetActiveAIModels() ([]*models.AIModel, error) {
	db.MainMutex.RLock()
	defer db.MainMutex.RUnlock()

	query := `SELECT id, post_key, ollama_model_name, display_name, description, is_active, is_default, sort_order, created_at, updated_at
	          FROM ai_models
	          WHERE is_active = 1
	          ORDER BY sort_order ASC, display_name ASC`

	rows, err := retryableQuery(db.mainDB, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var ollaamaModels []*models.AIModel
	for rows.Next() {
		om := &models.AIModel{}
		err := rows.Scan(
			&om.ID, &om.PostKey, &om.OllamaModelName, &om.DisplayName, &om.Description,
			&om.IsActive, &om.IsDefault, &om.SortOrder,
			&om.CreatedAt, &om.UpdatedAt,
		)
		if err != nil {
			return nil, err
		}
		ollaamaModels = append(ollaamaModels, om)
	}

	return ollaamaModels, rows.Err()
}

// GetDefaultAIModel returns the default AI model for new chats
func (db *Database) GetDefaultAIModel() (*models.AIModel, error) {
	db.MainMutex.RLock()
	defer db.MainMutex.RUnlock()

	query := `SELECT id, post_key, ollama_model_name, display_name, description, is_active, is_default, sort_order, created_at, updated_at
	          FROM ai_models
	          WHERE is_default = 1 AND is_active = 1
	          LIMIT 1`

	model := &models.AIModel{}
	err := retryableQueryRowScan(db.mainDB, query, nil,
		&model.ID, &model.PostKey, &model.OllamaModelName, &model.DisplayName, &model.Description,
		&model.IsActive, &model.IsDefault, &model.SortOrder,
		&model.CreatedAt, &model.UpdatedAt,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			// Fallback to first active model if no default set
			return db.GetFirstActiveAIModel()
		}
		return nil, err
	}

	return model, nil
}

// GetFirstActiveAIModel returns the first active AI model as fallback
func (db *Database) GetFirstActiveAIModel() (*models.AIModel, error) {
	db.MainMutex.RLock()
	defer db.MainMutex.RUnlock()

	query := `SELECT id, post_key, ollama_model_name, display_name, description, is_active, is_default, sort_order, created_at, updated_at
	          FROM ai_models
	          WHERE is_active = 1
	          ORDER BY sort_order ASC, display_name ASC
	          LIMIT 1`

	model := &models.AIModel{}
	err := retryableQueryRowScan(db.mainDB, query, nil,
		&model.ID, &model.PostKey, &model.OllamaModelName, &model.DisplayName, &model.Description,
		&model.IsActive, &model.IsDefault, &model.SortOrder,
		&model.CreatedAt, &model.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}

	return model, nil
}

// GetAIModelByPostKey returns an AI model by its post_key
func (db *Database) GetAIModelByPostKey(postKey string) (*models.AIModel, error) {
	db.MainMutex.RLock()
	defer db.MainMutex.RUnlock()

	query := `SELECT id, post_key, ollama_model_name, display_name, description, is_active, is_default, sort_order, created_at, updated_at
	          FROM ai_models
	          WHERE post_key = ?`

	model := &models.AIModel{}
	err := retryableQueryRowScan(db.mainDB, query, []interface{}{postKey},
		&model.ID, &model.PostKey, &model.OllamaModelName, &model.DisplayName, &model.Description,
		&model.IsActive, &model.IsDefault, &model.SortOrder,
		&model.CreatedAt, &model.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}

	return model, nil
}

// CreateAIModel creates a new AI model
func (db *Database) CreateAIModel(postKey, ollamaModelName, displayName, description string, isActive, isDefault bool, sortOrder int) (*models.AIModel, error) {
	db.MainMutex.Lock()
	defer db.MainMutex.Unlock()

	query := `INSERT INTO ai_models (post_key, ollama_model_name, display_name, description, is_active, is_default, sort_order)
	          VALUES (?, ?, ?, ?, ?, ?, ?)`

	result, err := retryableExec(db.mainDB, query, postKey, ollamaModelName, displayName, description, isActive, isDefault, sortOrder)
	if err != nil {
		return nil, err
	}

	id, err := result.LastInsertId()
	if err != nil {
		return nil, err
	}

	model := &models.AIModel{
		ID:              int(id),
		PostKey:         postKey,
		OllamaModelName: ollamaModelName,
		DisplayName:     displayName,
		Description:     description,
		IsActive:        isActive,
		IsDefault:       isDefault,
		SortOrder:       sortOrder,
		CreatedAt:       time.Now(),
		UpdatedAt:       time.Now(),
	}

	return model, nil
}

// UpdateAIModel updates an existing AI model
func (db *Database) UpdateAIModel(id int, ollamaModelName, displayName, description string, isActive, isDefault bool, sortOrder int) error {
	db.MainMutex.Lock()
	defer db.MainMutex.Unlock()

	log.Printf("Updating AI model ID %d: %s, %s, %s, active=%t, default=%t, sort_order=%d", id, ollamaModelName, displayName, description, isActive, isDefault, sortOrder)

	query := `UPDATE ai_models
	          SET ollama_model_name = ?, display_name = ?, description = ?, is_active = ?, is_default = ?, sort_order = ?, updated_at = CURRENT_TIMESTAMP
	          WHERE id = ?`

	_, err := retryableExec(db.mainDB, query, ollamaModelName, displayName, description, isActive, isDefault, sortOrder, id)
	return err
}

// SetDefaultAIModel sets a model as default (and unsets others)
func (db *Database) SetDefaultAIModel(id int) error {
	db.MainMutex.Lock()
	defer db.MainMutex.Unlock()

	return retryableTransactionExec(db.mainDB, func(tx *sql.Tx) error {
		// First, unset all defaults
		_, err := tx.Exec("UPDATE ai_models SET is_default = 0")
		if err != nil {
			return err
		}

		// Then set the specified model as default
		_, err = tx.Exec("UPDATE ai_models SET is_default = 1, updated_at = CURRENT_TIMESTAMP WHERE id = ?", id)
		return err
	})
}

// DeleteAIModel deletes an AI model (if it's not the last active one)
func (db *Database) DeleteAIModel(id int) error {
	db.MainMutex.Lock()
	defer db.MainMutex.Unlock()
	query := `DELETE FROM ai_models WHERE id = ?`
	_, err := retryableExec(db.mainDB, query, id)
	return err
}

// GetAllAIModels returns all AI models (for admin interface)
func (db *Database) GetAllAIModels() ([]*models.AIModel, error) {
	db.MainMutex.RLock()
	defer db.MainMutex.RUnlock()

	query := `SELECT id, post_key, ollama_model_name, display_name, description, is_active, is_default, sort_order, created_at, updated_at
	          FROM ai_models
	          ORDER BY sort_order ASC, display_name ASC`

	rows, err := retryableQuery(db.mainDB, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var ollaamaModels []*models.AIModel
	for rows.Next() {
		om := &models.AIModel{}
		err := rows.Scan(
			&om.ID, &om.PostKey, &om.OllamaModelName, &om.DisplayName, &om.Description,
			&om.IsActive, &om.IsDefault, &om.SortOrder,
			&om.CreatedAt, &om.UpdatedAt,
		)
		if err != nil {
			return nil, err
		}
		ollaamaModels = append(ollaamaModels, om)
	}

	return ollaamaModels, rows.Err()
}
