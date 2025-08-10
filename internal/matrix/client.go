package matrix

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/go-while/go-pugleaf/internal/models"
)

type MatrixClient struct {
	Homeserver  string
	AccessToken string
	UserID      string
	Client      *http.Client
}

type MatrixMessage struct {
	MsgType       string `json:"msgtype"`
	Body          string `json:"body"`
	Format        string `json:"format,omitempty"`
	FormattedBody string `json:"formatted_body,omitempty"`
}

type MatrixEvent struct {
	Type     string      `json:"type"`
	Content  interface{} `json:"content"`
	StateKey string      `json:"state_key,omitempty"`
}

type RoomCreateRequest struct {
	Name          string         `json:"name"`
	Topic         string         `json:"topic,omitempty"`
	Preset        string         `json:"preset,omitempty"`
	Visibility    string         `json:"visibility"`
	RoomAliasName string         `json:"room_alias_name,omitempty"`
	InitialState  []MatrixEvent  `json:"initial_state,omitempty"`
	PowerLevels   map[string]int `json:"power_level_content_override,omitempty"`
}

type RoomCreateResponse struct {
	RoomID string `json:"room_id"`
}

func NewMatrixClient(homeserver, accessToken, userID string) *MatrixClient {
	return &MatrixClient{
		Homeserver:  homeserver,
		AccessToken: accessToken,
		UserID:      userID,
		Client:      &http.Client{Timeout: 30 * time.Second},
	}
}

func (mc *MatrixClient) CreateRoom(newsgroup *models.Newsgroup) (string, error) {
	createReq := RoomCreateRequest{
		Name:          newsgroup.Name,
		Topic:         newsgroup.Description,
		Preset:        "public_chat",
		Visibility:    "public",
		RoomAliasName: fmt.Sprintf("newsgroup_%s", newsgroup.Name),
	}

	data, err := json.Marshal(createReq)
	if err != nil {
		return "", fmt.Errorf("failed to marshal room creation request: %w", err)
	}

	url := fmt.Sprintf("%s/_matrix/client/r0/createRoom?access_token=%s",
		mc.Homeserver, mc.AccessToken)

	resp, err := mc.Client.Post(url, "application/json", bytes.NewBuffer(data))
	if err != nil {
		return "", fmt.Errorf("failed to create room: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	var createResp RoomCreateResponse
	if err := json.NewDecoder(resp.Body).Decode(&createResp); err != nil {
		return "", fmt.Errorf("failed to decode response: %w", err)
	}

	return createResp.RoomID, nil
}

func (mc *MatrixClient) SendMessage(roomID string, message *MatrixMessage) error {
	data, err := json.Marshal(message)
	if err != nil {
		return fmt.Errorf("failed to marshal message: %w", err)
	}

	txnID := fmt.Sprintf("%d", time.Now().UnixNano())
	url := fmt.Sprintf("%s/_matrix/client/r0/rooms/%s/send/m.room.message/%s?access_token=%s",
		mc.Homeserver, url.PathEscape(roomID), txnID, mc.AccessToken)

	resp, err := mc.Client.Post(url, "application/json", bytes.NewBuffer(data))
	if err != nil {
		return fmt.Errorf("failed to send message: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	return nil
}

func (mc *MatrixClient) ArticleToMessage(article *models.Article) *MatrixMessage {
	htmlBody := fmt.Sprintf(`<h3>%s</h3>
<p><strong>From:</strong> %s</p>
<p><strong>Date:</strong> %s</p>
<p><strong>Message-ID:</strong> %s</p>
<hr>
<pre>%s</pre>`,
		article.Subject,
		article.FromHeader,
		article.DateSent.Format(time.RFC3339),
		article.MessageID,
		article.BodyText)

	plainBody := fmt.Sprintf("Subject: %s\nFrom: %s\nDate: %s\nMessage-ID: %s\n\n%s",
		article.Subject,
		article.FromHeader,
		article.DateSent.Format(time.RFC3339),
		article.MessageID,
		article.BodyText)

	return &MatrixMessage{
		MsgType:       "m.text",
		Body:          plainBody,
		Format:        "org.matrix.custom.html",
		FormattedBody: htmlBody,
	}
}

func (mc *MatrixClient) SendArticle(roomID string, article *models.Article) error {
	message := mc.ArticleToMessage(article)
	return mc.SendMessage(roomID, message)
}
