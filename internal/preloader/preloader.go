// Package preloader handles loading precompiled data like newsgroup descriptions
package preloader

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/go-while/go-pugleaf/internal/database"
	"github.com/go-while/go-pugleaf/internal/models"
)

// LoadNewsgroupsFromActive loads newsgroups from an NNTP active file
// File format: <groupname> <highwater> <lowwater> <status>
func LoadNewsgroupsFromActive(ctx context.Context, db *database.Database, filePath string) error {
	file, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("preloader failed to open active file: %w", err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	lineNum := 0
	createdCount := 0
	updatedCount := 0

	log.Printf("PreLoader: Loading newsgroups from active file %s...", filePath)

	for scanner.Scan() {
		lineNum++
		line := strings.TrimSpace(scanner.Text())

		// Skip empty lines
		if line == "" {
			continue
		}

		// Split by space - format: groupname highwater lowwater status
		parts := strings.Fields(line)
		if len(parts) < 4 {
			log.Printf("PreLoader: Warning: Invalid active line format at line %d: %s", lineNum, line)
			continue
		}

		groupName := strings.TrimSpace(parts[0])
		//highWaterStr := parts[1]
		//lowWaterStr := parts[2]
		status := strings.TrimSpace(parts[3])

		// Skip if group name is empty
		if groupName == "" {
			log.Printf("PreLoader: Warning: Empty group name at line %d", lineNum)
			continue
		}
		/*
			// Parse high/low water marks
			highWater, err := strconv.Atoi(highWaterStr)
			if err != nil {
				log.Printf("PreLoader: Warning: Invalid high water mark '%s' at line %d", highWaterStr, lineNum)
				continue
			}

			lowWater, err := strconv.Atoi(lowWaterStr)
			if err != nil {
				log.Printf("PreLoader: Warning: Invalid low water mark '%s' at line %d", lowWaterStr, lineNum)
				continue
			}
		*/
		// Check if newsgroup already exists
		highWater, lowWater := 0, 1 // Default values if not specified
		_, err := db.GetNewsgroupByName(groupName)
		if err != nil {
			// Newsgroup doesn't exist, create it
			newsgroup := &models.Newsgroup{
				Name:         groupName,
				Active:       true,
				Description:  "",
				LastArticle:  int64(highWater),
				MessageCount: int64(highWater - lowWater + 1),
				ExpiryDays:   0, // Default no expiry
				MaxArticles:  0, // Default no limit
				HighWater:    highWater,
				LowWater:     lowWater,
				Status:       status,
				CreatedAt:    time.Now(),
				// Note: UpdatedAt will be set only when articles are processed via batch
			}

			if err := db.InsertNewsgroup(newsgroup); err != nil {
				log.Printf("PreLoader: Warning: Failed to create newsgroup %s: %v", groupName, err)
				continue
			}
			createdCount++
		} else {
			// Newsgroup exists - DO NOT UPDATE watermarks from active file!
			// The active file is only for initial creation, not for updating existing newsgroups
			// Existing newsgroups have their own article counts and state in the database
			log.Printf("PreLoader: Newsgroup %s already exists, skipping (not updating from active file)", groupName)
		}

		// Log progress every 5000 lines
		if lineNum%5000 == 0 {
			log.Printf("Processed %d lines, created %d newsgroups, updated %d", lineNum, createdCount, updatedCount)
		}
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("preloader error reading active file: %w", err)
	}

	log.Printf("PreLoader: Finished loading from active file: processed %d lines, created %d newsgroups, updated %d existing",
		lineNum, createdCount, updatedCount)

	return nil
}

// LoadNewsgroupsFromDescriptions loads newsgroups from a descriptions file and creates them if they don't exist
// File format: <groupname>\t<description>
func LoadNewsgroupsFromDescriptions(ctx context.Context, db *database.Database, filePath string, createMissing bool) error {
	file, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("preloader failed to open newsgroups descriptions file: %w", err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	lineNum := 0
	createdCount := 0
	updatedCount := 0

	log.Printf("PreLoader: Loading newsgroups from descriptions file %s (createMissing=%v)...", filePath, createMissing)

	for scanner.Scan() {
		lineNum++
		line := strings.TrimSpace(scanner.Text())

		// Skip empty lines
		if line == "" {
			continue
		}

		// Split by tab
		parts := strings.SplitN(line, "\t", 2)
		if len(parts) != 2 {
			log.Printf("PreLoader: Warning: Invalid line format at line %d: %s", lineNum, line)
			continue
		}

		groupName := strings.TrimSpace(parts[0])
		description := strings.TrimSpace(parts[1])

		// Skip if group name is empty
		if groupName == "" {
			log.Printf("PreLoader: Warning: Empty group name at line %d", lineNum)
			continue
		}

		// Check if newsgroup already exists
		existing, err := db.GetNewsgroupByName(groupName)
		if err != nil {
			// Newsgroup doesn't exist
			if createMissing {
				// Create new newsgroup with description
				newsgroup := &models.Newsgroup{
					Name:         groupName,
					Active:       true,
					Description:  description,
					LastArticle:  0,
					MessageCount: 0,
					ExpiryDays:   0, // Default no expiry
					MaxArticles:  0, // Default no limit
					HighWater:    0,
					LowWater:     1,
					Status:       "y", // Default to moderated posting allowed
					CreatedAt:    time.Now(),
					// Note: UpdatedAt will be set only when articles are processed via batch
				}

				if err := db.InsertNewsgroup(newsgroup); err != nil {
					log.Printf("PreLoader: Warning: Failed to create newsgroup %s: %v", groupName, err)
					continue
				}
				createdCount++
			}
			// If createMissing is false, skip non-existing groups
			continue
		}

		// Newsgroup exists, update description if needed
		if existing.Description == "" || existing.Description != description {
			if err := db.UpdateNewsgroupDescription(groupName, description); err != nil {
				log.Printf("PreLoader: Warning: Failed to update description for newsgroup %s: %v", groupName, err)
				continue
			}
			updatedCount++
		}

		// Log progress every 5000 lines
		if lineNum%5000 == 0 {
			log.Printf("Processed %d lines, created %d newsgroups, updated %d descriptions", lineNum, createdCount, updatedCount)
		}
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("preloader error reading newsgroups descriptions file: %w", err)
	}

	log.Printf("PreLoader: Finished loading from descriptions file: processed %d lines, created %d newsgroups, updated %d descriptions",
		lineNum, createdCount, updatedCount)

	return nil
}

// LoadNewsgroupDescriptions loads newsgroup descriptions from a file (backward compatibility)
// File format: <groupname>\t<description>
func LoadNewsgroupDescriptions(ctx context.Context, db *database.Database, filePath string) error {
	return LoadNewsgroupsFromDescriptions(ctx, db, filePath, false)
}

// LoadNewsgroupsFromFiles loads newsgroups from standard NNTP files
// activeFile: path to active file (groupname highwater lowwater status)
// descFile: path to descriptions file (groupname\tdescription) - optional, can be empty
// createFromDesc: whether to create missing newsgroups from description file
func LoadNewsgroupsFromFiles(ctx context.Context, db *database.Database, activeFile, descFile string, createFromDesc bool) error {
	var errors []string

	// Load from active file if provided
	if activeFile != "" {
		if _, err := os.Stat(activeFile); err == nil {
			log.Printf("PreLoader: Loading newsgroups from active file %s...", activeFile)
			if err := LoadNewsgroupsFromActive(ctx, db, activeFile); err != nil {
				errors = append(errors, fmt.Sprintf("active file: %v", err))
			}
		} else {
			log.Printf("PreLoader: Active file not found at %s, skipping", activeFile)
		}
	}

	// Load from descriptions file if provided
	if descFile != "" {
		if _, err := os.Stat(descFile); err == nil {
			log.Printf("PreLoader: Loading descriptions from %s (createMissing=%v)...", descFile, createFromDesc)
			if err := LoadNewsgroupsFromDescriptions(ctx, db, descFile, createFromDesc); err != nil {
				errors = append(errors, fmt.Sprintf("descriptions file: %v", err))
			}
		} else {
			log.Printf("PreLoader: Descriptions file not found at %s, skipping", descFile)
		}
	}

	if len(errors) > 0 {
		return fmt.Errorf("preloader encountered errors: %s", strings.Join(errors, "; "))
	}

	return nil
}

// RepairNewsgroupWatermarks repairs newsgroup watermarks corrupted by previous preloader runs
// This function recalculates correct watermarks from actual articles in group databases
func RepairNewsgroupWatermarks(ctx context.Context, db *database.Database) error {
	log.Printf("PreLoader: Starting repair of corrupted newsgroup watermarks...")

	// Get all newsgroups
	newsgroups, err := db.MainDBGetAllNewsgroups()
	if err != nil {
		return fmt.Errorf("failed to get newsgroups: %w", err)
	}

	repairedCount := 0
	errorCount := 0

	for _, newsgroup := range newsgroups {
		// Get group database to check actual articles
		groupDBs, err := db.GetGroupDBs(newsgroup.Name)
		if err != nil {
			log.Printf("PreLoader: Failed to get group DBs for %s: %v", newsgroup.Name, err)
			errorCount++
			continue
		}

		// Calculate correct watermarks from actual articles
		var maxArticle, minArticle, articleCount int64

		// Get max article number
		err = groupDBs.DB.QueryRow("SELECT COALESCE(MAX(article_num), 0) FROM articles").Scan(&maxArticle)
		if err != nil {
			log.Printf("PreLoader: Failed to get max article for %s: %v", newsgroup.Name, err)
			groupDBs.Return(db)
			errorCount++
			continue
		}

		// Get min article number
		err = groupDBs.DB.QueryRow("SELECT COALESCE(MIN(article_num), 1) FROM articles").Scan(&minArticle)
		if err != nil {
			log.Printf("PreLoader: Failed to get min article for %s: %v", newsgroup.Name, err)
			groupDBs.Return(db)
			errorCount++
			continue
		}

		// Get article count
		err = groupDBs.DB.QueryRow("SELECT COUNT(*) FROM articles").Scan(&articleCount)
		if err != nil {
			log.Printf("PreLoader: Failed to get article count for %s: %v", newsgroup.Name, err)
			groupDBs.Return(db)
			errorCount++
			continue
		}

		groupDBs.Return(db)

		// If no articles, set defaults
		if articleCount == 0 {
			maxArticle = 0
			minArticle = 1
		}

		// Update newsgroup with correct values - preserve existing settings but fix watermarks
		newsgroup.LastArticle = maxArticle
		newsgroup.MessageCount = articleCount
		newsgroup.HighWater = int(maxArticle)
		newsgroup.LowWater = int(minArticle)
		// Note: UpdatedAt will be set only when articles are processed via batch

		err = db.UpdateNewsgroup(newsgroup)
		if err != nil {
			log.Printf("PreLoader: Failed to update watermarks for %s: %v", newsgroup.Name, err)
			errorCount++
			continue
		}

		log.Printf("PreLoader: Repaired %s - Articles: %d, HighWater: %d, LowWater: %d",
			newsgroup.Name, articleCount, maxArticle, minArticle)
		repairedCount++

		// Log progress every 100 newsgroups
		if repairedCount%100 == 0 {
			log.Printf("PreLoader: Repaired %d newsgroups, %d errors", repairedCount, errorCount)
		}
	}

	log.Printf("PreLoader: Finished repairing watermarks - Repaired: %d, Errors: %d", repairedCount, errorCount)

	if errorCount > 0 {
		return fmt.Errorf("repair completed with %d errors", errorCount)
	}

	return nil
}
