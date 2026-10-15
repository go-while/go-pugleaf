# Flat File Import Tool

This tool imports usenet articles (potentially billions) from a flat file structure into sharded SQLite databases.

## File Structure Expected

The tool expects articles to be stored in the following structure:

```
/mnt/xfshead/[0-f]/[0-f]/[0-f]/[hash].head
/mnt/xfsbody/[0-f]/[0-f]/[0-f]/[hash].body
```

Where:
- `[0-f]` is one hexadecimal digit per directory level (16 directories per level, 4096 leaf directories)
- `[hash]` is the hash of the message-ID without the 3 characters used by the directories
  (61 characters for a 64-character hash). At least 32 characters are required, and the first 3
  must be hex digits; other files are skipped and logged.
- Every `.head` file needs a `.body` file with the same name in the matching body directory;
  a head without a body is skipped (counted as `Skipped`).

## Database Output

Creates SQLite databases named `[0-f][0-f].db` (e.g., `00.db`, `01.db`, ..., `ff.db`) in `-db`.
Each table has this layout:

```sql
CREATE TABLE IF NOT EXISTS articles_0000 (
    messageid_hash CHAR(58) PRIMARY KEY,
    head TEXT NOT NULL,
    body TEXT NOT NULL
);
-- ... articles_0000 through articles_ffff
```

Databases and tables are created on first use: a database file is opened when the first article
routed to it is written, and a table is created (`CREATE TABLE IF NOT EXISTS`) when the first
article for it arrives. Nothing is created up front, so a database holds only the tables that
received articles (at most 65,536).

The head and body files are stored verbatim: line endings (CRLF or LF) are kept and no newline
is added.

**Sharding Strategy:**
- **Database selection**: first 2 directory levels (`dir1` + `dir2`) → 256 databases (00.db - ff.db)
- **Table selection**: third directory level + first 3 characters of the file name hash
  (`dir3` + `hash[0:3]`, lower-cased) → up to 65,536 tables per database (articles_0000 - articles_ffff)
- **Stored hash**: the rest of the file name hash (`hash[3:]`, 58 characters for a 61-character file name hash)

**Example:**
- Path: `/mnt/xfshead/a/b/c/defg123456789...xyz.head`
- Database: `ab.db`
- Table: `articles_cdef`
- Stored hash: `g123456789...xyz` (58 chars)

```
/mnt/xfs/a/b/c/d e f 123456789...xyz.(head|body)
         │ │ │ │ │ │
         │ │ │ │ │ └─ 6th characters (table routing) -1 = 58 remaining in hash
         │ │ │ │ └─ 5th characters (table routing) -1 = 59 remaining in hash
         │ │ │ └─ 4th character (table routing) -1 = 60 remaining in hash
         │ │ └─── 3rd directory level (table routing) -1 = 61 remaining in hash [result is the actual existing hashed filename[0:61].(head|body)]
         │ └───── 2nd directory level (database routing) -1 = 62 remaining in hash
         └─────── 1st directory level (database routing) -1 = 63 remaining in hash
```

## Usage

### Basic Usage
```bash
./build_import_flat-files.sh        # writes build/import-flat-files
./build/import-flat-files
```

### With Custom Paths
```bash
./build/import-flat-files \
    -head /mnt/xfshead \
    -body /mnt/xfsbody \
    -db ./imported_articles \
    -workers 16
```

### Options

- `-head PATH`: Path to head files directory (default: `/mnt/xfshead`)
- `-body PATH`: Path to body files directory (default: `/mnt/xfsbody`)
- `-db PATH`: Path for SQLite database files (default: `./imported_articles`)
- `-workers N`: Number of worker goroutines (default: 32)
- `-update`: Update mode: check every article in the database first and skip those already
  stored (default: false). Without it, already stored articles are ignored by the insert
  (`INSERT OR IGNORE`), so a plain re-run is safe too; `-update` saves reading their files.
- `-dry-run`: Scan and read the files without writing articles (default: false). Combined with
  `-update`, the existence check still opens (and creates) the databases and tables.
- `-verbose`: Verbose logging (default: false)

### Examples

**Dry run to estimate scope:**
```bash
./build/import-flat-files -dry-run -verbose
```

**High performance import:**
```bash
./build/import-flat-files \
    -workers 32 \
    -head /mnt/xfshead \
    -body /mnt/xfsbody \
    -db /fast/ssd/articles
```

**Resume interrupted import:**
```bash
./build/import-flat-files -update
```

## Performance Characteristics

- **Batch Processing**: each worker writes transactions of up to 10,000 articles; a batch is also
  written when the worker moves on to another database
- **Parallel Workers**: one scanner walks the directories in order and feeds `-workers` workers
- **Memory Efficient**: each file is read whole, one article at a time per worker
- **Database Optimization**: WAL mode, `synchronous = OFF`, larger cache and mmap
- **Progress Reporting**: reports progress and memory usage every 30 seconds

## Expected Performance

Rough estimates, not measured with the current code:
- **Processing Rate**: ~1000-5000 articles/second
- **Memory Usage**: ~100-500 MB RAM
- **Disk I/O**: sequential reads and batch writes

For billions of articles, expect the import to take several days to weeks depending on:
- Storage I/O performance (especially with hundreds of thousands of files per directory)
- Number of workers
- Target database storage speed

## Database Size Estimation

Each article will consume approximately:
- **Head**: 1-5 KB average
- **Body**: 5-50 KB average
- **Total per article**: ~6-55 KB
- **For 1 billion articles**: ~6-55 TB total database size

## Monitoring

The tool logs every 30 seconds:
- `Processed` (newly inserted articles; with `-dry-run` every article read), `Errors`, `Skipped`,
  and in `-update` mode `Existing`
- Articles per second and elapsed time
- Memory usage

At the end it logs the totals, the total time and the average rate.

## Error Handling

- **Missing Files**: a head without a body file is skipped (`Skipped`)
- **Invalid file names**: files whose name or directories do not match the expected layout are skipped and logged
- **Read errors**: the article is counted as an error and the import continues
- **Database Errors**: a failed insert counts that article as an error and the rest of the batch
  is still committed; a failed begin or commit counts the whole batch. Only opening a database and
  creating a table retry on busy/locked errors.
- **Duplicates**: articles already stored are counted as existing, not as errors (the `Existing`
  count is only logged in `-update` mode)
- **Interruption**: there is no signal handler; an interrupted import loses the open batches and
  can be resumed by running it again (with or without `-update`)

## Building

```bash
./build_import_flat-files.sh
# or
go build -o build/import-flat-files ./cmd/import-flat-files
```

## Dependencies

- Go (see `go.mod` for the version)
- github.com/mattn/go-sqlite3
