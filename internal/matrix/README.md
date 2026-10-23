# Matrix Bridge

This package implements Matrix protocol support for bridging NNTP newsgroups to Matrix rooms.

> **Status: stub, not wired in.** Nothing reaches this package at runtime: the `-enable-matrix`,
> `-matrix-homeserver`, `-matrix-accesstoken` and `-matrix-userid` flags are commented out in
> `cmd/web/main.go`, nothing calls `processor.EnableBridges` (so `Processor.BridgeManager` stays nil), and the
> `BridgeArticle` call in `internal/processor/threading.go` is commented out. The client code itself does
> real HTTP calls (create room, send message) when called directly.

## Features

- **Matrix Client**: Minimal Matrix client (create room, send `m.room.message`; nothing else)
- **Room Management**: Room creation for newsgroups on `RegisterNewsgroup` (the newsgroup → room map lives in memory only; existing rooms are not looked up)
- **Rich Formatting**: HTML and plain text message support
- **Real-time Bridging**: Live article streaming to Matrix rooms (planned; the processor hook is commented out)
- **Error Handling**: Errors are returned and logged (no retry or backoff)

## Components

### `client.go`
Core Matrix client implementation:
- `MatrixClient` - Main client instance with HTTP API
- `MatrixMessage` - Rich message formatting (HTML + plain text)
- `RoomCreateRequest/Response` - Room management
- `ArticleToMessage()` - Converts NNTP articles to Matrix messages
- `CreateRoom`, `SendMessage`, `SendArticle` - the HTTP calls (30 s client timeout)

### `bridge.go`
Bridge management:
- `Bridge` - Manages room registration and article bridging
- Thread-safe operations with mutex protection
- Enable/disable functionality
- Room mapping (newsgroup → Matrix room ID)

## Usage

```go
// Create bridge
bridge := matrix.NewBridge("https://matrix.org", "access_token", "@user:matrix.org")
bridge.Enable()

// Register newsgroup (creates Matrix room)
newsgroup := &models.Newsgroup{Name: "comp.lang.go", Description: "Go programming"}
bridge.RegisterNewsgroup(newsgroup)

// Bridge article (sends to Matrix room)
article := &models.Article{MessageID: "123@example.com", Subject: "Hello", BodyText: "World"}
bridge.BridgeArticle(article, "comp.lang.go")
```

## Matrix Room Features

Each newsgroup gets its own Matrix room with:
- **Room Name**: Newsgroup name (e.g., "comp.lang.go")
- **Room Topic**: Newsgroup description
- **Public Visibility**: Discoverable in room directory
- **Room Alias**: `#newsgroup_comp.lang.go:homeserver.com`

## Message Format

Articles are formatted as rich Matrix messages (`msgtype` `m.text`, `format` `org.matrix.custom.html`). Subject,
From, Message-ID and body are inserted into the HTML without escaping:

### HTML Format
```html
<h3>Hello World</h3>
<p><strong>From:</strong> user@example.com</p>
<p><strong>Date:</strong> 2025-07-09T12:00:00Z</p>
<p><strong>Message-ID:</strong> 123@example.com</p>
<hr>
<pre>Article body content here...</pre>
```

### Plain Text Format
```
Subject: Hello World
From: user@example.com
Date: 2025-07-09T12:00:00Z
Message-ID: 123@example.com

Article body content here...
```

## Configuration

### Prerequisites
1. Matrix account on a homeserver (matrix.org, etc.)
2. Access token (obtain via Element or API)
3. User ID in format `@username:homeserver.com`

### Enable via command line (planned: the flags are commented out in `cmd/web/main.go`, so `web` rejects them today):
```bash
./web --enable-matrix \
      --matrix-homeserver=https://matrix.org \
      --matrix-accesstoken=your_access_token \
      --matrix-userid=@your_user:matrix.org
```

The intended wiring is `processor.EnableBridges(&processor.BridgeConfig{MatrixEnabled: true, MatrixHomeserver: ..., MatrixAccessToken: ..., MatrixUserID: ...})`
(`internal/processor/bridges.go`); `NewBridgeManager` creates the bridge only when homeserver and access token are both set.

### Getting Access Token

#### Via Element Web Client:
1. Login to Element
2. Settings → Help & About → Access Token
3. Copy the token (starts with `syt_` or `mda_`)

#### Via API:
```bash
curl -XPOST -d '{"type":"m.login.password", "user":"username", "password":"password"}' \
     "https://matrix.org/_matrix/client/r0/login"
```

## Matrix API Endpoints Used

- `POST /_matrix/client/r0/createRoom` - Create newsgroup rooms
- `POST /_matrix/client/r0/rooms/{roomId}/send/m.room.message/{txnId}` - Send articles

The access token is passed as the `access_token` query parameter, not as an `Authorization` header.

## Room Settings

Created rooms have these settings:
- **Preset**: `public_chat` (anyone can join)
- **Visibility**: `public` (listed in directory)
- **History Visibility**: Inherited from homeserver
- **Join Rules**: `public` (implied by the preset, not set explicitly)

## Error Handling

Every non-200 reply becomes `unexpected status code: <n>`; there is no special handling of:
- **Rate Limiting**: HTTP 429 responses (no retry, no backoff)
- **Authentication**: Invalid/expired tokens
- **Permission**: Insufficient room permissions
- **Network**: Connection timeouts and failures (30 s client timeout, returned as an error)

## Security Considerations

- **Access Token**: Store securely, has full account access
- **Room Permissions**: Bot user should have moderator rights
- **Rate Limits**: Respect Matrix homeserver rate limits
- **Content Policy**: Follow homeserver content policies

## TODO

- [ ] Wire the bridge into `cmd/web` (flags, `EnableBridges`) and the processor (`BridgeArticle` call)
- [ ] Reuse existing rooms (alias lookup) and persist the newsgroup → room map
- [ ] Escape article fields in the HTML message
- [ ] Support for Matrix → NNTP bridging (replies)
- [ ] Rate limiting and backoff strategies
- [ ] End-to-end encryption support
- [ ] Moderation tools integration
- [ ] Threading support for Matrix replies
- [ ] Media attachment support
- [ ] Webhook support for real-time events
