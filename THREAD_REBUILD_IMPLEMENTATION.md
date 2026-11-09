# Thread Rebuild Implementation

## After updating to the per-group root rule: rebuild once

The threading rule changed (plan `thread-orphan-followups`): an article whose `References`
name no article **of this group** now starts a thread in this group, with or without
`References`. Articles that were filed before the fix stay unthreaded - they are stored and
readable by number and message-id, but they are in no `threads` row, so the group's thread
list does not show them. Nothing repairs that in the background.

So after updating, with the services stopped (fetcher, nntp-server, web: they write the same
group databases), run the rebuild once:

```bash
# all groups, in one go
./recover-db -data /path/to/data -group '$all' -rebuild-threads -force

# or one group at a time (same result, resumable by hand)
./recover-db -data /path/to/data -group comp.lang.go -rebuild-threads -force
```

`-force` skips the confirmation prompt; add `-v` for the per-step timers. The rebuild deletes
and rewrites `threads`, `thread_cache`, `cached_trees` and `tree_stats` of each group and
recomputes `articles.reply_count`; it never touches the articles themselves.

## Usage Examples

### 1. Rebuild threads for a specific newsgroup

```bash
# Rebuild threads for comp.lang.go
./recover-db -data /path/to/data -group comp.lang.go -rebuild-threads -v

# Rebuild threads for all newsgroups
./recover-db -data /path/to/data -group '$all' -rebuild-threads -v
```

### 2. Repair database with automatic thread rebuilding

```bash
# Check and repair with automatic thread rebuilding when needed
./recover-db -data /path/to/data -group comp.lang.go -repair -v
```

### 3. Check consistency first, then rebuild if needed

```bash
# First check what needs fixing
./recover-db -data /path/to/data -group comp.lang.go -v

# Then rebuild threads if orphaned threads are found
./recover-db -data /path/to/data -group comp.lang.go -rebuild-threads -v
```

## The threading rule (per group)

An article's thread is decided **per group**, not from its headers alone:

- its **parent** is the nearest `References` entry (reading the header from the last entry
  backwards) that exists in this group; if no entry exists here, the article has no parent;
- an article without a parent is a **thread root** in this group, `References` or not. That
  covers the follow-up whose thread lives in another group, the article whose parent expired
  or was never carried, and the one whose parent has not arrived yet;
- `depth` is the **parent's depth + 1**, not the position of the reference in the header:
  trimmed `References` (RFC 5537 3.4.4) make the position wrong;
- a reference **cycle** (including an article referencing itself) is broken where it is
  detected: that article becomes a thread root. One log line per group counts them.

Intake (`internal/database/db_batch.go`) applies the same rule when an article is filed, so a
group whose articles arrive in order gets the same rows from both sides. Two deliberate
differences:

- the rebuild **merges** an orphan under a parent that only arrived later (the out-of-order
  import case), while intake leaves the orphan a root - re-parenting every arrival would need
  a "who references me" scan that has no index;
- what makes a reference eligible as a parent: intake needs it to be threaded already (it has
  a `threads` row), the rebuild only needs it to exist in `articles`. After a full rebuild the
  two coincide, because then every article of the group has a `threads` row.

Row shapes written by both:

| | `root_article` | `parent_article` | `child_article` | `depth` |
|---|---|---|---|---|
| root | the article | `NULL` | the article | `0` |
| reply | its thread root | its parent | the article | parent's `depth` + 1 |

## Process Flow

### Thread Rebuild Process:

1. **Validation**: Check newsgroup exists and has articles
2. **Cleanup**: Delete all existing thread-related data:
   - `tree_stats` table
   - `cached_trees` table
   - `thread_cache` table
   - `threads` table
3. **Mapping**: Build the message-ID to article-number map of the whole group
4. **Pass 1**: For every article with `References`, the nearest reference that exists in this
   group becomes its parent (`parentOf`)
5. **Pass 2 / roots**: Resolve root and depth by following the parents (memoized, with the
   cycle guard above) and insert every **root**: its `threads` row and its `thread_cache` row.
   Roots go in first because a group filed out of order has roots with a higher article number
   than their children, and a child can only be appended to a `thread_cache` row that exists
6. **Pass 2 / replies**: Insert the **replies** chunk by chunk: the `threads` row and the
   append to their root's `thread_cache` row - `child_articles` sorted by article number (as
   intake writes it), `message_count`, and `last_child_number` / `last_activity` as the
   highest article number and the newest date over the root and its children (the value
   `cmd/fix-thread-activity` computes)
7. **Reply counts**: `articles.reply_count` = the number of direct children of each article,
   from the resolved parents; the reset of the group and the new values are written in one
   transaction, so the column is never left zeroed
8. **Reporting**: Generate comprehensive rebuild report, plus one `[RESCAN]` log line per
   group with its article count, roots, replies and duration


## Technical Details

### Memory Management
- Processes articles in chunks of `RescanBatchSize` (default: 1000), paged by `article_num`
- Holds three whole-group maps while it runs: message-id to article number, article number to
  parent, and the resolved root/depth memo. A group that fits today still fits
- Releases resources after each chunk

### Error Handling
- A failed `threads` or `reply_count` write aborts the group's rebuild and is reported in the
  rebuild report (the group keeps the cleared tables: run the rebuild again)
- A failed `thread_cache` write is logged and does not abort: the `threads` rows are the
  source a later rebuild re-derives the cache from
- The inserts and the `reply_count` update go through the `sqlite_retry.go` helpers, so a
  locked database is retried; the table clearing and the `thread_cache` children update still
  use a plain transaction of their own, and every error path rolls its transaction back
- Graceful handling of missing references (they simply make the article a thread root here)
- An article whose `date_sent` is NULL, or text that neither date format matches, is threaded
  with the zero date (and counted in one log line per group) instead of aborting the rebuild

### Performance Optimizations
- Efficient SQL queries with proper indexing
- Batch inserts for thread relationships
- Minimal memory footprint during processing
- Progress reporting for long operations

## Files Modified

1. **`internal/database/db_rescan.go`**:
   - `RebuildThreadsFromScratch()`: the rebuild itself
   - `threadRebuildState` with `buildThreadParentMap()` (pass 1) and `placeOf()` /
     `climbToRoot()` (pass 2, memo and cycle guard)
   - `rebuildThreadRoots()` / `rebuildThreadReplies()` and their insert helpers
     (`insertThreadRoots`, `insertThreadReplies`, `bumpThreadReplyCounts`)
   - `batchInitializeThreadCache()` / `updateThreadCacheWithChildren()`: the `thread_cache`
     rows of the roots and their children
   - `parseReferences()` utility function
   - `ThreadRebuildReport` struct and methods

2. **`cmd/recover-db/main.go`**:
   - Added `--rebuild-threads` command line flag
   - Added standalone thread rebuild mode
   - Added automatic thread rebuilding after repairs
   - Enhanced help text and output formatting

## Future Enhancements

1. **Performance improvements**:
   - Parallel processing for very large newsgroups
   - Configurable batch sizes
   - Memory usage optimization

2. **Additional features**:
   - Incremental thread rebuilding (only rebuild changed threads)
   - Thread validation without full rebuild
   - Integration with web interface for manual triggering

3. **Monitoring**:
   - Thread rebuild scheduling
   - Automatic detection of thread corruption
   - Performance metrics and alerts

## Testing

The rule and the row shapes above are covered by
`internal/database/thr_rebuild_test.go` (`go test -race ./internal/database/ -run ThrRebuild`):
unknown references, trimmed references, reply chains, out-of-order input, cycles, a
self-reference, `reply_count` and paging over several chunks.

Against a database, on a **scratch data root** (never `./data`, `data_ram` or `data_local`):

```bash
# Test with a small newsgroup first
./recover-db -data ./data-test-threads -group test.newsgroup -rebuild-threads -v

# Test repair with thread rebuilding
./recover-db -data ./data-test-threads -group test.newsgroup -repair -v

# Test with all newsgroups (use carefully!)
./recover-db -data ./data-test-threads -group '$all' -rebuild-threads -v
```
