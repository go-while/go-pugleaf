# Thread Rebuild Implementation
## Usage Examples

### 1. Rebuild threads for a specific newsgroup

```bash
# Rebuild threads for comp.lang.go
./recover-db -db /path/to/data -group comp.lang.go -rebuild-threads -v

# Rebuild threads for all newsgroups
./recover-db -db /path/to/data -group '$all' -rebuild-threads -v
```

### 2. Repair database with automatic thread rebuilding

```bash
# Check and repair with automatic thread rebuilding when needed
./recover-db -db /path/to/data -group comp.lang.go -repair -v
```

### 3. Check consistency first, then rebuild if needed

```bash
# First check what needs fixing
./recover-db -db /path/to/data -group comp.lang.go -v

# Then rebuild threads if orphaned threads are found
./recover-db -db /path/to/data -group comp.lang.go -rebuild-threads -v
```

## Process Flow

### Thread Rebuild Process:

1. **Validation**: Check newsgroup exists and has articles
2. **Cleanup**: Delete all existing thread-related data:
   - `tree_stats` table
   - `cached_trees` table
   - `thread_cache` table
   - `threads` table
3. **Mapping**: Build message-ID to article-number lookup table
4. **Threading**: Process articles in batches:
   - Parse References header for each article
   - Identify thread roots (no references)
   - Find parent articles and calculate depth
   - Insert thread relationships
5. **Reporting**: Generate comprehensive rebuild report


## Technical Details

### Memory Management
- Processes articles in configurable batches (default: 25,000)
- Builds message-ID mapping incrementally to handle large newsgroups
- Releases resources after each batch

### Error Handling
- Continues processing even if individual articles fail
- Reports all errors in final report
- Transactions ensure database consistency
- Graceful handling of missing references

### Performance Optimizations
- Efficient SQL queries with proper indexing
- Batch inserts for thread relationships
- Minimal memory footprint during processing
- Progress reporting for long operations

## Files Modified

1. **`internal/database/db_rescan.go`**:
   - Added `RebuildThreadsFromScratch()` function
   - Added `processThreadBatch()` helper function
   - Added `parseReferences()` utility function
   - Added `ThreadRebuildReport` struct and methods
   - Added necessary imports (strings, time)

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

Test the implementation with:

```bash
# Test with a small newsgroup first
./recover-db -db ./data -group test.newsgroup -rebuild-threads -v

# Test repair with thread rebuilding
./recover-db -db ./data -group test.newsgroup -repair -v

# Test with all newsgroups (use carefully!)
./recover-db -db ./data -group '$all' -rebuild-threads -v
```