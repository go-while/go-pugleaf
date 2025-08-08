# Flat File Import Tool

This tool imports billions of usenet articles from a flat file structure into SQLite databases.

## File Structure Expected

The tool expects articles to be stored in the following structure:

```
/mnt/xfshead/[0-f]/[0-f]/[0-f]/[hash].head
/mnt/xfsbody/[0-f]/[0-f]/[0-f]/[hash].body
```

Where:
- `[0-f]` represents hexadecimal directories (0-15)
- `[hash]` is the 61-character hash of the message-ID
- Each directory contains approximately 431k files

## Database Output

Creates SQLite databases named `[0-f][0-f].db` (e.g., `00.db`, `01.db`, ..., `ff.db`) containing:

```sql
CREATE TABLE articles_0000 (
    messageid_hash CHAR(58) PRIMARY KEY,
    head TEXT NOT NULL,
    body TEXT NOT NULL
);
-- ... articles_0001 through articles_ffff (65536 tables per database)
```

**Sharding Strategy:**
- **Database selection**: First 2 directory levels (`dir1` + `dir2`) → 256 databases (00.db - ff.db)
- **Table selection**: Third directory level + first 3 chars of hash (`dir3` + `hash[0:3]`) → 65536 tables per database (articles_0000 - articles_ffff)
- **Stored hash**: Remaining 58 characters of hash (`hash[3:]`) stored in database


**Example:**
- Path: `/mnt/xfshead/a/b/c/defg123456789...xyz.head`
- Database: `ab.db`
- Table: `articles_cdef`
- Stored hash: `g123456789...xyz` (58 chars)

/mnt/xfs/a/b/c/d e f 123456789...xyz.(head|body)
         │ │ │ │ │ │
         │ │ │ │ │ └─ 6th characters (table routing) -1 = 58 remaining in hash
         │ │ │ │ └─ 5th characters (table routing) -1 = 59 remaining in hash
         │ │ │ └─ 4th character (table routing) -1 = 60 remaining in hash
         │ │ └─── 3rd directory level (table routing) -1 = 61 remaining in hash [result is the actual existing hashed filename[0:61].(head|body)]
         │ └───── 2nd directory level (database routing) -1 = 62 remaining in hash
         └─────── 1st directory level (database routing) -1 = 63 remaining in hash

## Usage

### Basic Usage
```bash
go run cmd/import-flat-files/main.go
```

### With Custom Paths
```bash
go run cmd/import-flat-files/main.go \
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
- `-update`: Update mode: only import missing articles (default: false)
- `-dry-run`: Don't write to database, just scan files (default: false)
- `-verbose`: Verbose logging (default: false)

### Examples

**Dry run to estimate scope:**
```bash
go run cmd/import-flat-files/main.go -dry-run -verbose
```

**High performance import:**
```bash
go run cmd/import-flat-files/main.go \
    -workers 32 \
    -head /mnt/xfshead \
    -body /mnt/xfsbody \
    -db /fast/ssd/articles
```

**Resume interrupted import:**
```bash
go run cmd/import-flat-files/main.go -update
```

## Performance Characteristics

- **Batch Processing**: Uses transactions of 10,000 articles for optimal performance
- **Parallel Workers**: Configurable worker count for parallel processing
- **Memory Efficient**: Processes files sequentially within each worker
- **Database Optimization**: Uses WAL mode, optimized PRAGMA settings
- **Progress Reporting**: Reports progress every 30 seconds

## Expected Performance

With 8 workers on modern hardware:
- **Processing Rate**: ~1000-5000 articles/second
- **Memory Usage**: ~100-500 MB RAM
- **Disk I/O**: Optimized for sequential reads and batch writes

For billions of articles, expect the import to take several days to weeks depending on:
- Storage I/O performance (especially for 431k files per directory)
- Number of worker threads
- Target database storage speed

## Database Size Estimation

Each article will consume approximately:
- **Head**: 1-5 KB average
- **Body**: 5-50 KB average
- **Total per article**: ~6-55 KB
- **For 1 billion articles**: ~6-55 TB total database size

## Monitoring

The tool provides real-time statistics:
- Articles processed per second
- Total processed/errors/skipped
- Memory usage
- Elapsed time

## Error Handling

- **Missing Files**: Logs and skips articles missing head or body files
- **Database Errors**: Retries and logs failed inserts
- **Memory Management**: Periodic garbage collection during large batches
- **Graceful Shutdown**: Can be interrupted and resumed

## Database Distribution

Articles are distributed across:
- **256 databases** (00.db to ff.db) based on first 2 hex characters of hash
- **256 tables per database** (articles_000 to articles_fff) based on characters 3-5 of hash

This two-level sharding provides excellent load distribution and query performance while keeping resource usage reasonable with only 256 database connections.

## Building

```bash
go build -o import-flat-files cmd/import-flat-files/main.go
```

## Dependencies

- Go 1.19+
- github.com/mattn/go-sqlite3
