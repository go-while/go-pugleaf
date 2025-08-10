package main

import (
	"bufio"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// ThemeCategory represents a thematic grouping of hierarchies
type ThemeCategory struct {
	Name        string
	Description string
	Hierarchies []string
}

// parseHierarchiesFromSQL parses the SQL file to extract hierarchies by theme
func parseHierarchiesFromSQL(sqlFile string) ([]ThemeCategory, error) {
	file, err := os.Open(sqlFile)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var categories []ThemeCategory
	var currentCategory *ThemeCategory

	// Regex to match comment lines that define categories
	categoryRegex := regexp.MustCompile(`^-- (.+?) \(.*?\)$`)
	simpleCommentRegex := regexp.MustCompile(`^-- (.+)$`)
	insertRegex := regexp.MustCompile(`^\('([^']+)'`)

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())

		// Check for category comment
		if strings.HasPrefix(line, "-- ") && !strings.Contains(line, "go-pugleaf:") && !strings.Contains(line, "Insert some") {
			// Extract category name
			var categoryName string
			if matches := categoryRegex.FindStringSubmatch(line); len(matches) > 1 {
				categoryName = matches[1]
			} else if matches := simpleCommentRegex.FindStringSubmatch(line); len(matches) > 1 {
				categoryName = matches[1]
			}

			if categoryName != "" {
				// Create new category
				currentCategory = &ThemeCategory{
					Name:        sanitizeCategoryName(categoryName),
					Description: categoryName,
					Hierarchies: []string{},
				}
				categories = append(categories, *currentCategory)
			}
		}

		// Check for INSERT statements to extract hierarchy names
		if strings.HasPrefix(line, "('") && currentCategory != nil {
			if matches := insertRegex.FindStringSubmatch(line); len(matches) > 1 {
				hierarchy := matches[1]
				// Update the last category in the slice
				if len(categories) > 0 {
					categories[len(categories)-1].Hierarchies = append(categories[len(categories)-1].Hierarchies, hierarchy)
				}
			}
		}
	}

	return categories, scanner.Err()
}

// sanitizeCategoryName converts category names to filesystem-safe directory names
func sanitizeCategoryName(name string) string {
	// Convert to lowercase and replace problematic characters
	name = strings.ToLower(name)
	name = strings.ReplaceAll(name, "/", "-")
	name = strings.ReplaceAll(name, " ", "_")
	name = strings.ReplaceAll(name, ".", "")
	name = strings.ReplaceAll(name, "(", "")
	name = strings.ReplaceAll(name, ")", "")
	name = strings.ReplaceAll(name, ",", "")
	return name
}

func main() {
	sqlFile := "../../migrations/0003_main_create_hierarchies.sql"
	hierarchiesDir := "."
	themedDir := "themed"

	// Parse categories from SQL
	fmt.Println("Parsing hierarchy categories from SQL file...")
	categories, err := parseHierarchiesFromSQL(sqlFile)
	if err != nil {
		log.Fatalf("Failed to parse SQL file: %v", err)
	}

	fmt.Printf("Found %d theme categories\n", len(categories))

	// Create themed directory structure
	if err := os.MkdirAll(themedDir, 0755); err != nil {
		log.Fatalf("Failed to create themed directory: %v", err)
	}

	totalMoved := 0
	totalCategories := 0

	// Process each category
	for _, category := range categories {
		if len(category.Hierarchies) == 0 {
			continue
		}

		categoryDir := filepath.Join(themedDir, category.Name)
		if err := os.MkdirAll(categoryDir, 0755); err != nil {
			log.Printf("Failed to create category directory %s: %v", categoryDir, err)
			continue
		}

		// Create a README file for this category
		readmeFile := filepath.Join(categoryDir, "README.md")
		readme, err := os.Create(readmeFile)
		if err == nil {
			fmt.Fprintf(readme, "# %s\n\n", category.Description)
			fmt.Fprintf(readme, "This directory contains newsgroup hierarchies for: %s\n\n", category.Description)
			fmt.Fprintf(readme, "## Hierarchies in this category:\n\n")

			movedInCategory := 0
			for _, hierarchy := range category.Hierarchies {
				sourceFile := filepath.Join(hierarchiesDir, hierarchy+".active")
				if _, err := os.Stat(sourceFile); err == nil {
					targetFile := filepath.Join(categoryDir, hierarchy+".active")

					// Copy file instead of moving to preserve original
					if err := copyFile(sourceFile, targetFile); err != nil {
						log.Printf("Failed to copy %s: %v", sourceFile, err)
						continue
					}

					// Count lines in the file
					count := countLines(targetFile)
					fmt.Fprintf(readme, "- `%s.active` - %s (%d groups)\n", hierarchy, hierarchy, count)
					movedInCategory++
					totalMoved++
				}
			}
			readme.Close()

			if movedInCategory > 0 {
				fmt.Printf("Created category '%s' with %d hierarchies\n", category.Name, movedInCategory)
				totalCategories++
			} else {
				// Remove empty category directory
				os.RemoveAll(categoryDir)
			}
		}
	}

	// Create a master index
	indexFile := filepath.Join(themedDir, "INDEX.md")
	index, err := os.Create(indexFile)
	if err == nil {
		fmt.Fprintf(index, "# Usenet Hierarchy Categories\n\n")
		fmt.Fprintf(index, "This directory contains newsgroup hierarchies organized by theme/category.\n\n")
		fmt.Fprintf(index, "## Categories:\n\n")

		// List all created categories
		dirs, _ := os.ReadDir(themedDir)
		var categoryNames []string
		for _, dir := range dirs {
			if dir.IsDir() {
				categoryNames = append(categoryNames, dir.Name())
			}
		}
		sort.Strings(categoryNames)

		for _, catName := range categoryNames {
			catDir := filepath.Join(themedDir, catName)
			files, _ := os.ReadDir(catDir)
			activeCount := 0
			for _, file := range files {
				if strings.HasSuffix(file.Name(), ".active") {
					activeCount++
				}
			}
			fmt.Fprintf(index, "- [`%s/`](./%s/) - %d hierarchies\n", catName, catName, activeCount)
		}

		fmt.Fprintf(index, "\n## Statistics:\n\n")
		fmt.Fprintf(index, "- Total categories: %d\n", totalCategories)
		fmt.Fprintf(index, "- Total hierarchy files: %d\n", totalMoved)
		index.Close()
	}

	fmt.Printf("\nSummary:\n")
	fmt.Printf("Created %d themed categories\n", totalCategories)
	fmt.Printf("Organized %d hierarchy files\n", totalMoved)
	fmt.Printf("Files organized in: %s/\n", themedDir)
	fmt.Printf("See %s for an overview\n", filepath.Join(themedDir, "INDEX.md"))
}

// copyFile copies a file from src to dst
func copyFile(src, dst string) error {
	source, err := os.Open(src)
	if err != nil {
		return err
	}
	defer source.Close()

	destination, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer destination.Close()

	_, err = destination.ReadFrom(source)
	return err
}

// countLines counts the number of lines in a file
func countLines(filename string) int {
	file, err := os.Open(filename)
	if err != nil {
		return 0
	}
	defer file.Close()

	count := 0
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		count++
	}
	return count
}
