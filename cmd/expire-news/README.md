# expire-news

A comprehensive news article expiration and pruning tool for go-pugleaf NNTP server.

## Overview

The `expire-news` tool manages article lifecycle in newsgroups by providing two main operations:
- **Age-based expiry**: Remove articles older than specified days
- **Count-based pruning**: Remove oldest articles to respect maximum article limits

## Features

- **Dual Operation Modes**: Expiry by age and/or pruning by count
- **Flexible Group Selection**: Single groups, wildcards, or all groups
- **Safety Features**: Dry-run mode, force flag requirement, comprehensive logging
- **Performance Optimized**: Batch processing with SQLite parameter limit handling
- **Database Integration**: Respects per-group settings and updates counters automatically

## Usage

### Basic Commands

```bash
# Show help and examples
./expire-news -help

# Dry run to see what would be deleted
./expire-news -group '$all' -days 30 -dry-run

# Actually expire articles (requires -force)
./expire-news -group '$all' -days 30 -force

# Prune groups to respect max_articles limits
./expire-news -group '$all' -prune -force

# Combined expiry and pruning
./expire-news -group '$all' -days 30 -prune -force
```

### Command Line Options

| Flag | Description | Default |
|------|-------------|---------|
| `-group` | Newsgroup pattern (`$all`, `alt.*`, or specific group) | Required |
| `-days` | Delete articles older than N days (one of `-days`, `-respect-expiry` or `-prune` is required) | 0 (no age expiry) |
| `-prune` | Remove oldest articles to respect max_articles limit | false |
| `-respect-expiry` | Use per-group expiry_days from database | false |
| `-dry-run` | Preview mode - show what would be deleted | false |
| `-force` | Required for actual deletions | false |
| `-batch-size` | Articles to process per batch (1-10000) | 1000 |
| `-trim-history` | Also remove deleted articles from the message-id history index | false |
| `-data` | Data directory | `./data` |
| `-help` | Show usage examples and exit | false |

### Group Selection Patterns

- `$all` - Process all active newsgroups
- `alt.*` - Process all groups starting with "alt."
- `comp.lang.go` - Process specific newsgroup
- `news.*` - Process all groups starting with "news."

`$all` and wildcards (a name prefix followed by `*`) select only active groups. A single group
that does not exist or is inactive is an error (exit code 1).

## Operation Modes

### Age-Based Expiry

Removes articles older than specified days using the article's `date_sent` field.

```bash
# Expire articles older than 30 days in all groups
./expire-news -group '$all' -days 30 -force

# Use per-group expiry_days settings from database
./expire-news -group '$all' -respect-expiry -force
```

**Default Behavior**: Groups with `expiry_days = 0` are considered to have infinite retention (no expiry).
With `-respect-expiry` a group's `expiry_days` overrides `-days`, and groups with `expiry_days = 0`
are not expired by age even when `-days` is set.

### Count-Based Pruning

Removes oldest articles (by `article_num`) to keep groups under their `max_articles` limit.

```bash
# Prune all groups to respect their max_articles settings
./expire-news -group '$all' -prune -force

# Prune specific newsgroup pattern
./expire-news -group 'alt.*' -prune -force
```

**Default Behavior**: Groups with `max_articles = 0` are considered to have no count limit (keep forever).

**Pruning Logic**: If a group has 920 articles and `max_articles = 10`, it will delete articles 1-910 and keep articles 911-920 (the newest 10).

### History Index (`-trim-history`)

By default expired or pruned articles **stay in the message-id history index**
(`<data>/history`), INN-style "remember": the server keeps rejecting them as duplicates
when a peer or fetcher offers them again.

With `-trim-history` (live mode only, ignored with `-dry-run`) the message-ids of the
deleted articles are read inside the delete transaction and, after the commit, this
group's ID is removed from their history entries. An entry without any group left is
deleted. Crossposts stay in the index for their other groups.
The article numbers of the deleted rows are read too and passed with the removal; the
group's token is removed whatever number it held (also an old plain `groupID` token), and the
`groupID:articleNum` tokens of the other groups (local `Xref:`) stay as they are.

```bash
./expire-news -group '$all' -days 30 -force -trim-history
```

The whole index can also be trimmed later with `history-rebuild -trim`.

### Articles without date_sent

Articles with a NULL or unparseable `date_sent` are never expired by age. They are
counted and reported per group (`skipped N articles without a valid date_sent`);
`-prune` still removes them by article number.

## Database Schema Requirements

The tool expects newsgroups to have the following fields:

```sql
CREATE TABLE newsgroups (
    name TEXT PRIMARY KEY,
    expiry_days INTEGER DEFAULT 0,     -- 0 = infinite retention
    max_articles INTEGER DEFAULT 0,    -- 0 = no limit
    message_count INTEGER,
    last_article INTEGER,
    high_water INTEGER,
    low_water INTEGER,
    -- ... other fields
);
```

## Safety Features

1. **Dry-Run First**: Always test with `-dry-run` before using `-force`
2. **Force Required**: Actual deletions require explicit `-force` flag
3. **Batch Processing**: Handles large datasets without memory issues
4. **Transaction Safety**: Each batch is deleted in one group database transaction (articles,
   threads and the thread caches `tree_stats`, `cached_trees`, `thread_cache`); the spam flags of the
   deleted articles in the main database are removed afterwards
5. **Counter Updates**: Automatically updates the newsgroup's `message_count`, `last_article` and the
   water marks `LIST ACTIVE` prints (`high_water` = `last_article`, `low_water` = lowest remaining
   article; an emptied group keeps its place: `low_water` = next article number = `last_article` + 1)
6. **Progress Logging**: Detailed logging of operations and progress

## Performance Considerations

- **Batch Size**: Default 1000 articles per batch, adjustable with `-batch-size`
- **SQLite Limits**: Automatically chunks operations to stay under SQLite's parameter limits
- **Memory Management**: Processes articles in batches to avoid memory issues
- **Database Connections**: Properly manages and returns database connections

## Examples

### Daily Maintenance
```bash
# Combined daily maintenance: expire old articles and enforce limits
./expire-news -group '$all' -respect-expiry -prune -force
```

### Specific Group Management
```bash
# Clean up alt.* groups - expire after 7 days, prune each group to its max_articles
./expire-news -group 'alt.*' -days 7 -prune -force
```

### Testing Changes
```bash
# Always test first with dry-run
./expire-news -group 'comp.lang.go' -days 90 -prune -dry-run

# Then apply if results look correct
./expire-news -group 'comp.lang.go' -days 90 -prune -force
```

### Emergency Cleanup
```bash
# Aggressive cleanup for storage issues
./expire-news -group '$all' -days 14 -prune -force
```

## Building

```bash
# Build the tool
cd /path/to/go-pugleaf
go build -o build/expire-news ./cmd/expire-news

# Or use the build script
./build_expire-news.sh
```

## Output Example

```
2025/08/04 03:39:04 Starting go-pugleaf News Expiration Tool (version 1.0.0)
Found 150 newsgroups to process
DRY RUN MODE: No articles will actually be deleted
PRUNE MODE: Will respect max_articles limits
EXPIRY MODE: Will remove old articles

[1/150] Processing alt.binaries.test expiry (using command line: 30 days)
Expiring articles older than: 2025-07-05 03:39:04
Skipping alt.binaries.test pruning (no max_articles limit set)
Would process alt.binaries.test: expired 450 (age) (scanned 1200)

[2/150] Processing comp.lang.go expiry (using command line: 30 days)
Expiring articles older than: 2025-07-05 03:39:04
Pruning comp.lang.go to max 1000 articles
Group comp.lang.go has 1250 articles, need to remove 250 to stay under limit of 1000
Would process comp.lang.go: expired 12 (age), pruned 250 (count) (scanned 2500)

=== Expiration Summary ===
Would process 15420 articles total (scanned 125000 articles)
Use -force to actually perform deletions
```

## Error Handling

The tool handles various error conditions gracefully:
- With `$all` or a wildcard, inactive groups are not selected; a single `-group` that is missing
  or inactive stops the run (exit code 1)
- Database connection issues are logged and processing continues with other groups
- Invalid article data is logged but doesn't stop the process
- Transaction failures cause rollback and error reporting
- Ctrl+C stops after the current group; queued history removals are flushed and the
  databases are shut down cleanly
- Exit code: 0 ok, 1 errors, 130 interrupted

## Integration

This tool is designed to be run as part of regular maintenance:
- Cron jobs for automated cleanup
- Integration with monitoring systems
- Safe for concurrent operation with NNTP server
