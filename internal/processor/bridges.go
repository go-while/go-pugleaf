package processor

import (
	"log"

	"github.com/go-while/go-pugleaf/internal/fediverse"
	"github.com/go-while/go-pugleaf/internal/matrix"
	"github.com/go-while/go-pugleaf/internal/models"
)

type BridgeConfig struct {
	// Fediverse configuration
	FediverseEnabled bool   `json:"fediverse_enabled"`
	FediverseDomain  string `json:"fediverse_domain"`
	FediverseBaseURL string `json:"fediverse_base_url"`

	// Matrix configuration
	MatrixEnabled     bool   `json:"matrix_enabled"`
	MatrixHomeserver  string `json:"matrix_homeserver"`
	MatrixAccessToken string `json:"matrix_access_token"`
	MatrixUserID      string `json:"matrix_user_id"`
}

type BridgeManager struct {
	config          *BridgeConfig
	fediverseBridge *fediverse.Bridge
	matrixBridge    *matrix.Bridge
}

func NewBridgeManager(config *BridgeConfig) *BridgeManager {
	bm := &BridgeManager{
		config: config,
	}

	// Initialize Fediverse bridge if enabled
	if config.FediverseEnabled && config.FediverseDomain != "" && config.FediverseBaseURL != "" {
		bm.fediverseBridge = fediverse.NewBridge(config.FediverseDomain, config.FediverseBaseURL)
		bm.fediverseBridge.Enable()
		log.Printf("BridgeManager: Fediverse bridge initialized for domain %s", config.FediverseDomain)
	}

	// Initialize Matrix bridge if enabled
	if config.MatrixEnabled && config.MatrixHomeserver != "" && config.MatrixAccessToken != "" {
		bm.matrixBridge = matrix.NewBridge(config.MatrixHomeserver, config.MatrixAccessToken, config.MatrixUserID)
		bm.matrixBridge.Enable()
		log.Printf("BridgeManager: Matrix bridge initialized for homeserver %s", config.MatrixHomeserver)
	}

	if !config.FediverseEnabled && !config.MatrixEnabled {
		log.Printf("BridgeManager: All bridges disabled")
	}

	return bm
}

func (bm *BridgeManager) IsAnyBridgeEnabled() bool {
	return bm.config.FediverseEnabled || bm.config.MatrixEnabled
}

func (bm *BridgeManager) RegisterNewsgroup(newsgroup *models.Newsgroup) error {
	if !bm.IsAnyBridgeEnabled() {
		return nil
	}

	var err error

	// Register with Fediverse bridge
	if bm.fediverseBridge != nil && bm.fediverseBridge.IsEnabled() {
		if regErr := bm.fediverseBridge.RegisterNewsgroup(newsgroup); regErr != nil {
			log.Printf("BridgeManager: Failed to register newsgroup %s with Fediverse: %v", newsgroup.Name, regErr)
			err = regErr
		}
	}

	// Register with Matrix bridge
	if bm.matrixBridge != nil && bm.matrixBridge.IsEnabled() {
		if regErr := bm.matrixBridge.RegisterNewsgroup(newsgroup); regErr != nil {
			log.Printf("BridgeManager: Failed to register newsgroup %s with Matrix: %v", newsgroup.Name, regErr)
			err = regErr
		}
	}

	return err
}

func (bm *BridgeManager) BridgeArticle(article *models.Article, newsgroup string) {
	if !bm.IsAnyBridgeEnabled() {
		return
	}

	// Bridge to Fediverse
	if bm.fediverseBridge != nil && bm.fediverseBridge.IsEnabled() {
		if err := bm.fediverseBridge.BridgeArticle(article, newsgroup); err != nil {
			log.Printf("BridgeManager: Failed to bridge article %s to Fediverse: %v", article.MessageID, err)
		}
	}

	// Bridge to Matrix
	if bm.matrixBridge != nil && bm.matrixBridge.IsEnabled() {
		if err := bm.matrixBridge.BridgeArticle(article, newsgroup); err != nil {
			log.Printf("BridgeManager: Failed to bridge article %s to Matrix: %v", article.MessageID, err)
		}
	}
}

func (bm *BridgeManager) Close() {
	if bm.fediverseBridge != nil {
		bm.fediverseBridge.Disable()
	}
	if bm.matrixBridge != nil {
		bm.matrixBridge.Disable()
	}
	log.Printf("BridgeManager: All bridges disabled")
}
