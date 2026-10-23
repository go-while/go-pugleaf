# Fediverse Bridge

This package implements ActivityPub protocol support for bridging NNTP newsgroups to the Fediverse (Mastodon, Pleroma, etc.).

> **Status: stub, not wired in.** Nothing reaches this package at runtime: the `-enable-fediverse`,
> `-fediverse-domain` and `-fediverse-baseurl` flags are commented out in `cmd/web/main.go`, nothing calls
> `processor.EnableBridges` (so `Processor.BridgeManager` stays nil), and the `BridgeArticle` call in
> `internal/processor/threading.go` is commented out. Even when called directly, the bridge only builds
> actors, Notes and Create activities in memory and logs them; no activity is ever sent and no HTTP route serves them.

## Features

- **ActivityPub Server**: Creates newsgroups as ActivityPub Group actors (in memory; `ActivityPubServer` is not an HTTP server)
- **Article Bridging**: Converts NNTP articles to ActivityPub Notes (the Create activity is built and discarded: `// TODO: actually send the activity`)
- **Federation Ready**: Supports ActivityStreams vocabulary
- **HTTP Signatures**: Prepared for authenticated federation (TODO; no keys are generated, `PublicKeyPem` is the placeholder `"TODO: RSA public key"`)

## Components

### `activitypub.go`
Core ActivityPub protocol implementation:
- `ActivityPubServer` - Main server instance
- `Actor` - Represents newsgroups as Group actors
- `Note` - Represents articles as ActivityPub Notes
- `Activity` - Wraps actions (Create, Update, etc.)
- `SendActivity` - POSTs an activity to an inbox, unsigned; nothing calls it

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

Planned only: none of these routes is registered in `internal/web` (the IDs below are only the URLs the
actor and Note objects point to):

- `GET /newsgroups/{name}` - Newsgroup actor
- `GET /newsgroups/{name}/inbox` - Receive activities
- `GET /newsgroups/{name}/outbox` - Published activities
- `GET /newsgroups/{name}/followers` - Follower collection
- `GET /articles/{messageId}` - Individual article as Note

## Configuration

Planned command line (the flags exist in `cmd/web/main.go` but are commented out, so `web` rejects them today):
```bash
./web --enable-fediverse --fediverse-domain=your.domain.com --fediverse-baseurl=https://your.domain.com
```
The intended wiring is `processor.EnableBridges(&processor.BridgeConfig{FediverseEnabled: true, FediverseDomain: ..., FediverseBaseURL: ...})`
(`internal/processor/bridges.go`); `NewBridgeManager` creates the bridge only when domain and base URL are both set.

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

- [ ] Wire the bridge into `cmd/web` (flags, `EnableBridges`) and the processor (`BridgeArticle` call)
- [ ] Serve the actor/inbox/outbox/Note endpoints and deliver activities to followers
- [ ] Implement HTTP signatures for authentication
- [ ] Add webhook support for incoming activities
- [ ] Support for replies and threading
- [ ] WebFinger support for discovery
- [ ] Rate limiting and security measures
- [ ] Moderation tools integration
