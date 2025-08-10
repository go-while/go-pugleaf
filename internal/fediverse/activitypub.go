package fediverse

import (
	"bytes"
	"crypto/rsa"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/go-while/go-pugleaf/internal/models"
)

type ActivityPubServer struct {
	Domain     string
	PrivateKey *rsa.PrivateKey
	PublicKey  *rsa.PublicKey
	BaseURL    string
}

type Actor struct {
	Context           []interface{} `json:"@context"`
	ID                string        `json:"id"`
	Type              string        `json:"type"`
	PreferredUsername string        `json:"preferredUsername"`
	Name              string        `json:"name"`
	Summary           string        `json:"summary"`
	Inbox             string        `json:"inbox"`
	Outbox            string        `json:"outbox"`
	Followers         string        `json:"followers"`
	Following         string        `json:"following"`
	PublicKey         PublicKey     `json:"publicKey"`
}

type PublicKey struct {
	ID           string `json:"id"`
	Owner        string `json:"owner"`
	PublicKeyPem string `json:"publicKeyPem"`
}

type Activity struct {
	Context   []interface{} `json:"@context"`
	ID        string        `json:"id"`
	Type      string        `json:"type"`
	Actor     string        `json:"actor"`
	Object    interface{}   `json:"object"`
	Published time.Time     `json:"published"`
}

type Note struct {
	ID        string    `json:"id"`
	Type      string    `json:"type"`
	Summary   string    `json:"summary,omitempty"`
	Content   string    `json:"content"`
	Actor     string    `json:"actor"`
	To        []string  `json:"to"`
	CC        []string  `json:"cc,omitempty"`
	Published time.Time `json:"published"`
	Tag       []Tag     `json:"tag,omitempty"`
}

type Tag struct {
	Type string `json:"type"`
	Name string `json:"name"`
	Href string `json:"href,omitempty"`
}

func NewActivityPubServer(domain, baseURL string) *ActivityPubServer {
	return &ActivityPubServer{
		Domain:  domain,
		BaseURL: baseURL,
	}
}

func (aps *ActivityPubServer) CreateNewsgroupActor(newsgroup *models.Newsgroup) *Actor {
	actorID := fmt.Sprintf("%s/newsgroups/%s", aps.BaseURL, newsgroup.Name)

	return &Actor{
		Context: []interface{}{
			"https://www.w3.org/ns/activitystreams",
			"https://w3id.org/security/v1",
		},
		ID:                actorID,
		Type:              "Group",
		PreferredUsername: newsgroup.Name,
		Name:              newsgroup.Name,
		Summary:           newsgroup.Description,
		Inbox:             actorID + "/inbox",
		Outbox:            actorID + "/outbox",
		Followers:         actorID + "/followers",
		Following:         actorID + "/following",
		PublicKey: PublicKey{
			ID:           actorID + "#main-key",
			Owner:        actorID,
			PublicKeyPem: "TODO: RSA public key",
		},
	}
}

func (aps *ActivityPubServer) ArticleToNote(article *models.Article, newsgroup string) *Note {
	noteID := fmt.Sprintf("%s/articles/%s", aps.BaseURL, article.MessageID)
	actorID := fmt.Sprintf("%s/newsgroups/%s", aps.BaseURL, newsgroup)

	return &Note{
		ID:        noteID,
		Type:      "Note",
		Summary:   article.Subject,
		Content:   article.BodyText,
		Actor:     actorID,
		To:        []string{"https://www.w3.org/ns/activitystreams#Public"},
		CC:        []string{actorID + "/followers"},
		Published: article.DateSent,
	}
}

func (aps *ActivityPubServer) SendActivity(targetInbox string, activity *Activity) error {
	data, err := json.Marshal(activity)
	if err != nil {
		return fmt.Errorf("failed to marshal activity: %w", err)
	}

	req, err := http.NewRequest("POST", targetInbox, bytes.NewBuffer(data))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/activity+json")
	req.Header.Set("User-Agent", "go-pugleaf/1.0")

	// TODO: Add HTTP signature for authentication

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send activity: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	return nil
}
