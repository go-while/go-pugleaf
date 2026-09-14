# PostQueue System

This document describes the PostQueue system implementation for processing web-posted articles and forwarding them to remote NNTP servers in go-pugleaf.

## Architecture Overview

The PostQueue system consists of multiple components that handle the complete lifecycle of web-posted articles:

1. **Web-based Queue Worker** - Processes articles from web interface
2. **Database Tracking** - Tracks articles through the posting lifecycle
3. **NNTP Posting Tool** - Forwards articles to remote NNTP servers
4. **Provider Management** - Manages connections to multiple NNTP providers

## Components

### 1. Channel Definition (`internal/models/postqueue.go`)
- **Location**: `models` package to avoid import cycles
- **Purpose**: Global channel for passing articles from web interface to processor
- **Capacity**: 100 articles (buffered channel)

### 2. Web Interface (`internal/web/web_sitePostPage.go`)
- **Purpose**: Handles web posting forms and validation
- **Function**: `sitePostSubmit()` validates articles and puts them into `models.PostQueueChannel`
- **Features**:
  - Form validation (subject, body, newsgroups)
  - Newsgroup existence and activity checking
  - Article size limits
  - Reply threading support with quoted content

### 3. Web Queue Worker (`internal/processor/PostQueue.go`)
- **Purpose**: Background worker that processes articles from the web posting queue
- **Features**:
  - Graceful start/stop
  - Per-newsgroup processing
  - Uses existing threading system
  - Periodic health logging
  - Database tracking via `post_queue` table
  - Error handling with logging

### 4. Database Tracking (`internal/database/db_post_queue.go`)
- **Purpose**: Track articles through the posting lifecycle
- **Table**: `post_queue` - stores articles that need to be posted to remote servers
- **Fields**:
  - `id` - Unique identifier
  - `newsgroup_id` - Reference to newsgroup
  - `message_id` - Article message ID
  - `created` - Timestamp when queued
  - `posted_to_remote` - Boolean flag if posted to remote servers

### 5. NNTP Posting Tool (`cmd/post-queue/`)
- **Purpose**: Standalone tool that posts queued articles to remote NNTP servers
- **Features**:
  - Provider pool management
  - Concurrent posting to multiple providers
  - Proper NNTP POST command implementation
  - Header reconstruction and validation
  - Retry logic with configurable attempts and delays
  - Dry-run capability for testing
  - Graceful shutdown handling

## Usage

### Web Queue Worker

The PostQueueWorker is automatically started when the web server starts (if a processor is available):

```go
// In cmd/web/main.go
if proc != nil {
    postQueueWorker = processor.NewPostQueueWorker(proc)
    postQueueWorker.Start()
    log.Printf("[WEB]: PostQueueWorker started for web posting")
}
```

### NNTP Posting Tool

The post-queue tool is run separately to process articles and forward them to remote servers:

```bash
# Basic usage
./post-queue

# Advanced configuration
./post-queue -max-providers 5 -max-concurrent 3 -retry-attempts 5 -retry-delay 60s

# Testing mode
./post-queue -dry-run

# Continuous monitoring
./post-queue -check-interval 30s
```

#### Command Line Options

- `-max-providers INT` - Maximum number of providers to use (default: 10)
- `-max-concurrent INT` - Maximum concurrent posting operations per provider (default: 5)
- `-retry-attempts INT` - Number of retry attempts for failed posts (default: 3)
- `-retry-delay DURATION` - Delay between retry attempts (default: 30s)
- `-check-interval DURATION` - Interval for checking pending posts (default: 60s)
- `-dry-run` - Perform a dry run without actually posting
- `-help` - Show usage examples

## Flow

### Web Posting Flow
1. **User posts article** via web interface (`/SitePost`)
2. **Web handler validates** the article and newsgroups
3. **Article is queued** into `models.PostQueueChannel`
4. **Web worker processes** the article using `processor.processArticle()`
5. **Article is threaded** and stored in appropriate newsgroup databases
6. **Database entry created** in `post_queue` table for NNTP posting

### NNTP Posting Flow
1. **Tool queries** `post_queue` table for unposted articles (`posted_to_remote = 0`)
2. **Article retrieved** from local newsgroup database
3. **Headers reconstructed** using `common.ReconstructHeaders()`
4. **Article posted** to all enabled providers with `posting = true`
5. **Success tracking** - if posted to any provider, marked as `posted_to_remote = 1`
6. **Logging** of success/failure for each provider

## NNTP Implementation Details

### Header Reconstruction
- Uses `internal/common/headers.go` for consistent header processing
- Validates RFC compliance for Date headers
- Filters out duplicate and invalid headers
- Handles multi-line header continuation
- Ignores system headers (Message-ID, Subject, From, Date, References, Path, Xref)

### POST Command Implementation
- Proper NNTP POST protocol implementation in `internal/nntp/nntp-client-commands.go`
- Uses buffered writer with proper CRLF line endings
- Implements dot-stuffing for lines starting with "."
- Handles response codes (240 success, 441 failure)
- Graceful error handling and connection management

### Provider Management
- Supports multiple NNTP providers with individual configurations
- Connection pooling for efficient resource usage
- SSL/TLS support with proper certificate handling
- Proxy support (HTTP/SOCKS) for network restrictions
- Authentication with username/password
- Configurable connection limits per provider

## Error Handling

### Web Queue
- **Queue full**: User gets "Server is busy" error
- **Invalid newsgroups**: Form validation prevents submission
- **Processing errors**: Logged, article stored in database for retry

### NNTP Posting
- **Connection failures**: Logged, retry on next run
- **Authentication failures**: Logged, provider marked as problematic
- **Article rejection**: Logged with NNTP response code
- **Network timeouts**: Automatic retry with exponential backoff
- **Graceful shutdown**: In-progress operations completed before exit

## Configuration

### Provider Configuration
Providers are configured in the database with these key fields for posting:
- `enabled` - Must be true
- `posting` - Must be true to be used for posting
- `host`, `port` - NNTP server connection details
- `username`, `password` - Authentication credentials
- `ssl` - Use SSL/TLS connection
- `max_conns` - Maximum concurrent connections
- `proxy_*` - Proxy configuration if needed

### Performance Tuning
- `max-providers` - Limit concurrent providers to avoid overload
- `max-concurrent` - Control concurrency per provider
- `check-interval` - Balance responsiveness vs. system load
- Pool cleanup intervals - Manage connection lifecycle

## Monitoring and Debugging

### Logging
- Detailed logging of all posting attempts
- Success/failure tracking per provider
- Queue processing statistics
- Connection pool status
- Error categorization for troubleshooting

### Dry Run Mode
- Test configuration without actual posting
- Validate article reconstruction
- Check provider connectivity
- Verify queue processing logic

## Future Enhancements

- **Priority queuing**: High-priority articles posted first
- **Dead letter queue**: Failed articles stored for manual review
- **Metrics collection**: Prometheus/monitoring integration
- **Provider health monitoring**: Automatic failover for problematic providers
- **Bandwidth throttling**: Rate limiting per provider
- **Article deduplication**: Prevent duplicate posts across providers
- **Selective posting**: Route different newsgroups to different providers

## Import Cycle Solution

To avoid import cycles:
- **Channel**: Defined in `models` package (imported by both `web` and `processor`)
- **Worker**: Lives in `processor` package
- **Web interface**: Uses channel from `models` package
- **Common utilities**: Shared code in `internal/common` package
- **Database operations**: Centralized in `database` package

This ensures clean separation without circular dependencies while enabling code reuse across components.
