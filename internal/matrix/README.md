# Matrix Bridge

This package implements Matrix protocol support for bridging NNTP newsgroups to Matrix rooms.

## Features

- **Matrix Client**: Full Matrix client implementation
- **Room Management**: Automatic room creation for newsgroups
- **Rich Formatting**: HTML and plain text message support
- **Real-time Bridging**: Live article streaming to Matrix rooms
- **Error Handling**: Robust error handling and logging

## Components

### `client.go`
Core Matrix client implementation:
- `MatrixClient` - Main client instance with HTTP API
- `MatrixMessage` - Rich message formatting (HTML + plain text)
- `RoomCreateRequest/Response` - Room management
- `ArticleToMessage()` - Converts NNTP articles to Matrix messages

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

Articles are formatted as rich Matrix messages:

### HTML Format
```html
<h3>Subject: Hello World</h3>
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

### Enable via command line:
```bash
./web --enable-matrix \
      --matrix-homeserver=https://matrix.org \
      --matrix-accesstoken=your_access_token \
      --matrix-userid=@your_user:matrix.org
```

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

## Room Settings

Created rooms have these settings:
- **Preset**: `public_chat` (anyone can join)
- **Visibility**: `public` (listed in directory)
- **History Visibility**: Inherited from homeserver
- **Join Rules**: `public`

## Error Handling

The bridge handles common Matrix errors:
- **Rate Limiting**: HTTP 429 responses
- **Authentication**: Invalid/expired tokens
- **Permission**: Insufficient room permissions
- **Network**: Connection timeouts and failures

## Security Considerations

- **Access Token**: Store securely, has full account access
- **Room Permissions**: Bot user should have moderator rights
- **Rate Limits**: Respect Matrix homeserver rate limits
- **Content Policy**: Follow homeserver content policies

## TODO

- [ ] Support for Matrix → NNTP bridging (replies)
- [ ] Rate limiting and backoff strategies
- [ ] End-to-end encryption support
- [ ] Moderation tools integration
- [ ] Threading support for Matrix replies
- [ ] Media attachment support
- [ ] Webhook support for real-time events
