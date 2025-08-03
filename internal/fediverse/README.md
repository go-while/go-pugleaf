# Fediverse Bridge

This package implements ActivityPub protocol support for bridging NNTP newsgroups to the Fediverse (Mastodon, Pleroma, etc.).

## Features

- **ActivityPub Server**: Creates newsgroups as ActivityPub Group actors
- **Article Bridging**: Converts NNTP articles to ActivityPub Notes
- **Federation Ready**: Supports ActivityStreams vocabulary
- **HTTP Signatures**: Prepared for authenticated federation (TODO)

## Components

### `activitypub.go`
Core ActivityPub protocol implementation:
- `ActivityPubServer` - Main server instance
- `Actor` - Represents newsgroups as Group actors
- `Note` - Represents articles as ActivityPub Notes
- `Activity` - Wraps actions (Create, Update, etc.)

### `bridge.go`
Bridge management:
- `Bridge` - Manages newsgroup registration and article bridging
- Thread-safe operations with mutex protection
- Enable/disable functionality

## Usage

```go
// Create bridge
bridge := fediverse.NewBridge("example.com", "https://example.com")
bridge.Enable()

// Register newsgroup
newsgroup := &models.Newsgroup{Name: "comp.lang.go", Description: "Go programming"}
bridge.RegisterNewsgroup(newsgroup)

// Bridge article
article := &models.Article{MessageID: "123@example.com", Subject: "Hello", BodyText: "World"}
bridge.BridgeArticle(article, "comp.lang.go")
```

## ActivityPub Endpoints

When integrated, the following endpoints will be available:

- `GET /newsgroups/{name}` - Newsgroup actor
- `GET /newsgroups/{name}/inbox` - Receive activities
- `GET /newsgroups/{name}/outbox` - Published activities
- `GET /newsgroups/{name}/followers` - Follower collection
- `GET /articles/{messageId}` - Individual article as Note

## Configuration

Enable via command line:
```bash
./web --enable-fediverse --fediverse-domain=your.domain.com --fediverse-baseurl=https://your.domain.com
```

## Standards Compliance

- **ActivityPub**: W3C Recommendation for federated social networking
- **ActivityStreams 2.0**: Vocabulary for social activities
- **HTTP Signatures**: Authentication for server-to-server communication (planned)

## Example ActivityPub Objects

### Newsgroup Actor
```json
{
  "@context": ["https://www.w3.org/ns/activitystreams"],
  "id": "https://example.com/newsgroups/comp.lang.go",
  "type": "Group",
  "preferredUsername": "comp.lang.go",
  "name": "comp.lang.go",
  "summary": "Go programming language discussion",
  "inbox": "https://example.com/newsgroups/comp.lang.go/inbox",
  "outbox": "https://example.com/newsgroups/comp.lang.go/outbox"
}
```

### Article Note
```json
{
  "id": "https://example.com/articles/123@example.com",
  "type": "Note",
  "summary": "Hello World",
  "content": "This is my first post!",
  "actor": "https://example.com/newsgroups/comp.lang.go",
  "published": "2025-07-09T12:00:00Z"
}
```

## TODO

- [ ] Implement HTTP signatures for authentication
- [ ] Add webhook support for incoming activities
- [ ] Support for replies and threading
- [ ] WebFinger support for discovery
- [ ] Rate limiting and security measures
- [ ] Moderation tools integration
