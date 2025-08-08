# Legacy SQLite Database Importer

This tool imports legacy SQLite databases from RockSolid Light into the go-pugleaf database format.

## Features

- Imports articles from legacy SQLite databases (`*-articles.db3`)
- Imports thread data from legacy SQLite databases (`*-data.db3`)
- Converts legacy article format to go-pugleaf format
- Preserves article metadata (subject, author, date, message ID, etc.)
- Handles newsgroup-specific databases
- Optional import of RockSolid Light sections and groups

## Usage

### Basic SQLite Import

```bash
go run cmd/sqlite-importer/main.go -sqlite-dir /path/to/legacy-sqlite3/
```

### Full Import with Legacy Configuration

```bash
go run cmd/sqlite-importer/main.go \
  -sqlite-dir /path/to/legacy-sqlite3/ \
  -legacy-path /path/to/legacy/rocksolid/ \
  -data-dir ./data/
```

## Command Line Options

- `-sqlite-dir`: Directory containing legacy SQLite files (*.db3) - **Required**
- `-data-dir`: Directory to store new database files (default: `./data`)
- `-legacy-path`: Path to legacy RockSolid Light installation (optional)

## Database Structure

### Legacy SQLite Schema

The importer expects SQLite files with these schemas:

#### Articles Database (`*-articles.db3`)
```sql
CREATE TABLE articles(
     id INTEGER PRIMARY KEY,
     newsgroup TEXT,
     number TEXT UNIQUE,
     msgid TEXT UNIQUE,
     date TEXT,
     name TEXT,
     subject TEXT,
     search_snippet TEXT,
     article TEXT
);
```

#### Data Database (`*-data.db3`)
```sql
CREATE TABLE threads(
     id INTEGER PRIMARY KEY,
     headers TEXT
);
```

### Target go-pugleaf Schema

Articles are converted to the go-pugleaf format:

- `id` → `article_num` (converted to integer)
- `msgid` → `message_id`
- `subject` → `subject`
- `name` → `from_header`
- `date` → `date_sent` (converted from Unix timestamp)
- `article` → `body_text`
- Article length → `bytes`
- Line count → `lines`

## Example Output

```
2024/12/22 10:30:00 Starting SQLite import from: /path/to/legacy-sqlite3/
2024/12/22 10:30:00 Target database directory: ./data
2024/12/22 10:30:00 Found 2 SQLite database files
2024/12/22 10:30:00 Processing SQLite file: comp.programming-articles.db3
2024/12/22 10:30:01 Imported 1000 articles...
2024/12/22 10:30:05 SQLite articles import completed: 2541 imported, 0 skipped
2024/12/22 10:30:05 Processing SQLite file: comp.programming-data.db3
2024/12/22 10:30:06 SQLite threads import completed: 1 imported, 0 skipped
2024/12/22 10:30:06 Import process completed!
```

## Data Conversion Notes

1. **Timestamps**: Legacy Unix timestamps are converted to Go time.Time format
2. **Article Numbers**: String article numbers are converted to integers
3. **Newsgroups**: Each newsgroup gets its own database files in go-pugleaf
4. **Full-Text Search**: Legacy FTS data is not imported (will be rebuilt)
5. **Threading**: Thread headers (PHP serialized data) are logged but not processed yet

## Error Handling

- Articles with invalid data are skipped and logged
- Database connection errors are reported
- Missing or corrupted SQLite files are handled gracefully
- The import continues even if individual articles fail

## Performance

- Articles are imported in batches for better performance
- Progress is logged every 1000 articles
- Uses SQLite transactions for data integrity
- Duplicate articles are ignored (INSERT OR IGNORE)

## Dependencies

- Go 1.19+
- github.com/mattn/go-sqlite3
- go-pugleaf internal packages
