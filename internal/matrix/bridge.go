package matrix

import (
	"fmt"
	"log"
	"sync"

	"github.com/go-while/go-pugleaf/internal/models"
)

type Bridge struct {
	client  *MatrixClient
	enabled bool
	mutex   sync.RWMutex
	rooms   map[string]string // newsgroup -> room_id mapping
}

func NewBridge(homeserver, accessToken, userID string) *Bridge {
	return &Bridge{
		client:  NewMatrixClient(homeserver, accessToken, userID),
		enabled: false,
		rooms:   make(map[string]string),
	}
}

func (b *Bridge) Enable() {
	b.mutex.Lock()
	defer b.mutex.Unlock()
	b.enabled = true
	log.Printf("Matrix bridge enabled")
}

func (b *Bridge) Disable() {
	b.mutex.Lock()
	defer b.mutex.Unlock()
	b.enabled = false
	log.Printf("Matrix bridge disabled")
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

	roomID, err := b.client.CreateRoom(newsgroup)
	if err != nil {
		return fmt.Errorf("failed to create Matrix room for newsgroup %s: %w", newsgroup.Name, err)
	}

	b.rooms[newsgroup.Name] = roomID
	log.Printf("Created Matrix room %s for newsgroup %s", roomID, newsgroup.Name)

	return nil
}

func (b *Bridge) BridgeArticle(article *models.Article, newsgroup string) error {
	if !b.IsEnabled() {
		return nil
	}

	b.mutex.RLock()
	roomID, exists := b.rooms[newsgroup]
	b.mutex.RUnlock()

	if !exists {
		return fmt.Errorf("newsgroup %s not registered with Matrix bridge", newsgroup)
	}

	err := b.client.SendArticle(roomID, article)
	if err != nil {
		return fmt.Errorf("failed to send article to Matrix room: %w", err)
	}

	log.Printf("Bridged article %s to Matrix room %s", article.MessageID, roomID)
	return nil
}
