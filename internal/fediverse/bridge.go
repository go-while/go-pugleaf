package fediverse

import (
	"fmt"
	"log"
	"sync"

	"github.com/go-while/go-pugleaf/internal/models"
)

type Bridge struct {
	server  *ActivityPubServer
	enabled bool
	mutex   sync.RWMutex
	rooms   map[string]string // newsgroup -> actor mapping
}

func NewBridge(domain, baseURL string) *Bridge {
	return &Bridge{
		server:  NewActivityPubServer(domain, baseURL),
		enabled: false,
		rooms:   make(map[string]string),
	}
}

func (b *Bridge) Enable() {
	b.mutex.Lock()
	defer b.mutex.Unlock()
	b.enabled = true
	log.Printf("Fediverse bridge enabled")
}

func (b *Bridge) Disable() {
	b.mutex.Lock()
	defer b.mutex.Unlock()
	b.enabled = false
	log.Printf("Fediverse bridge disabled")
}

func (b *Bridge) IsEnabled() bool {
	b.mutex.RLock()
	defer b.mutex.RUnlock()
	return b.enabled
}

func (b *Bridge) RegisterNewsgroup(newsgroup *models.Newsgroup) error {
	if !b.IsEnabled() {
		return nil
	}

	b.mutex.Lock()
	defer b.mutex.Unlock()

	actor := b.server.CreateNewsgroupActor(newsgroup)
	actorID := actor.ID

	b.rooms[newsgroup.Name] = actorID
	log.Printf("Registered newsgroup %s with Fediverse actor: %s", newsgroup.Name, actorID)

	return nil
}

func (b *Bridge) BridgeArticle(article *models.Article, newsgroup string) error {
	if !b.IsEnabled() {
		return nil
	}

	b.mutex.RLock()
	actorID, exists := b.rooms[newsgroup]
	b.mutex.RUnlock()

	if !exists {
		return fmt.Errorf("newsgroup %s not registered with Fediverse bridge", newsgroup)
	}

	note := b.server.ArticleToNote(article, newsgroup)

	activity := &Activity{
		Context: []interface{}{
			"https://www.w3.org/ns/activitystreams",
		},
		ID:        fmt.Sprintf("%s/activities/%s", b.server.BaseURL, article.MessageID),
		Type:      "Create",
		Actor:     actorID,
		Object:    note,
		Published: article.DateSent,
	}

	// TODO: Send to followers' inboxes
	_ = activity // TODO: actually send the activity
	log.Printf("Created Fediverse activity for article %s in newsgroup %s", article.MessageID, newsgroup)

	return nil
}
