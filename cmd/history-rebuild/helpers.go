package main

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-while/go-pugleaf/internal/database"
	"github.com/go-while/go-pugleaf/internal/history"
	_ "github.com/mattn/go-sqlite3"
)

// rebuildProgressFileName is written into the history directory after each fully processed group
const rebuildProgressFileName = "rebuild.progress"

// historyFilePath returns the path of history database file dbIndex (0-15)
func historyFilePath(historyDir string, dbIndex int) string {
	return filepath.Join(historyDir, fmt.Sprintf("hashdb_%x.sqlite3", dbIndex))
}

// historyTableName returns the name of table tableIndex (0-255) inside a history database file
func historyTableName(tableIndex int) string {
	return fmt.Sprintf("_%02x", tableIndex)
}

// groupDBFilePath returns the on-disk path of a group database (same layout as database.GetGroupDB),
// so a group can be checked without GetGroupDB creating an empty file.
func groupDBFilePath(dataDir, groupName string) string {
	return filepath.Join(dataDir, "db", database.MD5Hash(groupName), database.SanitizeGroupName(groupName)+".db")
}

// readRebuildProgress returns the last fully processed group name ("" if there is no progress file)
func readRebuildProgress(historyDir string) (string, error) {
	data, err := os.ReadFile(filepath.Join(historyDir, rebuildProgressFileName))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

// writeRebuildProgress atomically stores the last fully processed group name (tmp file + rename)
func writeRebuildProgress(historyDir, groupName string) error {
	final := filepath.Join(historyDir, rebuildProgressFileName)
	tmp := final + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(groupName + "\n"); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, final)
}

// removeRebuildProgress deletes the progress file (no error if it does not exist)
func removeRebuildProgress(historyDir string) error {
	err := os.Remove(filepath.Join(historyDir, rebuildProgressFileName))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// skipGroupForResume reports whether groupName was already processed by a previous run.
// Groups are processed sorted by name, so everything up to and including lastDone is done.
func skipGroupForResume(groupName, lastDone string) bool {
	return lastDone != "" && groupName <= lastDone
}

// countGroupsInCSV returns the number of group IDs in a history newsgroups value ("" = 0)
func countGroupsInCSV(newsgroups string) int {
	if newsgroups == "" {
		return 0
	}
	return strings.Count(newsgroups, ",") + 1
}

// openHistoryFileReadOnly opens one history database file read-only
func openHistoryFileReadOnly(path string) (*sql.DB, error) {
	if _, err := os.Stat(path); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite3", "file:"+path+"?mode=ro&_busy_timeout=30000")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

// HistoryFileAnalysis holds the row counts of one history database file
type HistoryFileAnalysis struct {
	Path      string
	Missing   bool
	Rows      int64
	Tables    int
	MaxTable  string
	MaxRows   int64
	MinTable  string
	MinRows   int64
	TableRows []int64 // rows per table index
}

// HistoryAnalysis is the result of analyzeHistoryDir
type HistoryAnalysis struct {
	HistoryDir   string
	Files        []HistoryFileAnalysis
	TotalRows    int64
	MissingFiles int
	// GroupsHistogram maps "number of groups per message-id" -> number of message-ids
	GroupsHistogram map[int]int64
	MaxGroups       int
}

// analyzeHistoryDir scans all history database files read-only: rows per table / file and a histogram
// of the number of groups stored per message-id. Missing files are reported, not treated as errors.
func analyzeHistoryDir(historyDir string) (*HistoryAnalysis, error) {
	numDBs, tablesPerDB, _ := history.GetShardConfig(history.SHARD_16_256)
	res := &HistoryAnalysis{
		HistoryDir:      historyDir,
		GroupsHistogram: make(map[int]int64),
	}
	for dbIndex := 0; dbIndex < numDBs; dbIndex++ {
		fa := HistoryFileAnalysis{Path: historyFilePath(historyDir, dbIndex)}
		db, err := openHistoryFileReadOnly(fa.Path)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				fa.Missing = true
				res.MissingFiles++
				res.Files = append(res.Files, fa)
				continue
			}
			return nil, fmt.Errorf("open %s: %w", fa.Path, err)
		}
		fa.TableRows = make([]int64, tablesPerDB)
		for tableIndex := 0; tableIndex < tablesPerDB; tableIndex++ {
			tableName := historyTableName(tableIndex)
			var count int64
			if err := db.QueryRow("SELECT COUNT(*) FROM " + tableName).Scan(&count); err != nil {
				db.Close()
				return nil, fmt.Errorf("count %s in %s: %w", tableName, fa.Path, err)
			}
			if err := scanGroupsHistogram(db, tableName, res); err != nil {
				db.Close()
				return nil, fmt.Errorf("scan %s in %s: %w", tableName, fa.Path, err)
			}
			fa.TableRows[tableIndex] = count
			fa.Rows += count
			fa.Tables++
			if fa.Tables == 1 || count > fa.MaxRows {
				fa.MaxRows, fa.MaxTable = count, tableName
			}
			if fa.Tables == 1 || count < fa.MinRows {
				fa.MinRows, fa.MinTable = count, tableName
			}
		}
		db.Close()
		res.TotalRows += fa.Rows
		res.Files = append(res.Files, fa)
	}
	return res, nil
}

// scanGroupsHistogram adds the "groups per message-id" counts of one table to res
func scanGroupsHistogram(db *sql.DB, tableName string, res *HistoryAnalysis) error {
	rows, err := db.Query("SELECT newsgroups FROM " + tableName)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var newsgroups sql.NullString
		if err := rows.Scan(&newsgroups); err != nil {
			return err
		}
		n := countGroupsInCSV(newsgroups.String)
		res.GroupsHistogram[n]++
		if n > res.MaxGroups {
			res.MaxGroups = n
		}
	}
	return rows.Err()
}
