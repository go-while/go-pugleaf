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
| `-days` | Delete articles older than N days | 0 (disabled) |
| `-prune` | Remove oldest articles to respect max_articles limit | false |
| `-respect-expiry` | Use per-group expiry_days from database | false |
| `-dry-run` | Preview mode - show what would be deleted | false |
| `-force` | Required for actual deletions | false |
| `-batch-size` | Articles to process per batch | 1000 |
| `-nntphostname` | Server hostname | Required |

### Group Selection Patterns

- `$all` - Process all active newsgroups
- `alt.*` - Process all groups starting with "alt."
- `comp.lang.go` - Process specific newsgroup
- `news.*` - Process all groups starting with "news."

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

## Database Schema Requirements

The tool expects newsgroups to have the following fields:

```sql
CREATE TABLE newsgroups (
    name TEXT PRIMARY KEY,
    expiry_days INTEGER DEFAULT 0,     -- 0 = infinite retention
    max_articles INTEGER DEFAULT 0,    -- 0 = no limit
    message_count INTEGER,
    last_article INTEGER,
    -- ... other fields
);
```

## Safety Features

1. **Dry-Run First**: Always test with `-dry-run` before using `-force`
2. **Force Required**: Actual deletions require explicit `-force` flag
3. **Batch Processing**: Handles large datasets without memory issues
4. **Transaction Safety**: All deletions wrapped in database transactions
5. **Counter Updates**: Automatically updates newsgroup message counts
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
# Clean up alt.* groups - expire after 7 days, max 1000 articles each
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
Would process alt.binaries.test: expired 450 (age), pruned 0 (count) (scanned 1200)

[2/150] Processing comp.lang.go expiry (using group setting: 90 days)
Pruning comp.lang.go to max 1000 articles
Would process comp.lang.go: expired 12 (age), pruned 250 (count) (scanned 1262)

=== Expiration Summary ===
Would process 15420 articles total (scanned 125000 articles)
Use -force to actually perform deletions
```

## Error Handling

The tool handles various error conditions gracefully:
- Missing newsgroups or inactive groups are skipped
- Database connection issues are logged and processing continues with other groups
- Invalid article data is logged but doesn't stop the process
- Transaction failures cause rollback and error reporting

## Integration

This tool is designed to be run as part of regular maintenance:
- Cron jobs for automated cleanup
- Integration with monitoring systems
- Safe for concurrent operation with NNTP server
