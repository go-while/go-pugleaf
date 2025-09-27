package web

import (
	"fmt"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/go-while/go-pugleaf/internal/config"
	"github.com/go-while/go-pugleaf/internal/processor"
)

// SettingConfig defines configuration for each setting type
type SettingConfig struct {
	FormField    string
	ConfigKey    string
	Validator    func(string) error
	Processor    func(*WebServer, string) error
	SuccessMsg   func(string) string
	EmptyAllowed bool
}

// adminUpdateSettings handles all admin settings updates in a unified way
func (s *WebServer) adminUpdateSettings(c *gin.Context) {
	if !s.requireAdminAuth(c) {
		return
	}

	session := s.getWebSession(c)

	// Get the setting type from form
	settingType := strings.TrimSpace(c.PostForm("setting"))
	if settingType == "" {
		session.SetError("No setting type specified")
		c.Redirect(http.StatusSeeOther, "/admin?tab=settings")
		return
	}

	// Define setting configurations
	settings := map[string]SettingConfig{
		config.FORM_FIELD_HOSTNAME: {
			FormField:    config.FORM_FIELD_HOSTNAME,
			ConfigKey:    config.CFG_KEY_HOSTNAME,
			Validator:    s.validateHostname,
			Processor:    s.processHostname,
			SuccessMsg:   func(value string) string { return "NNTP hostname set to: " + value },
			EmptyAllowed: false,
		},
		config.FORM_FIELD_WEBPOSTSIZE: {
			FormField:    config.FORM_FIELD_WEBPOSTSIZE,
			ConfigKey:    config.CFG_KEY_WEBPOSTSIZE,
			Validator:    s.validateWebPostSize,
			Processor:    nil, // Uses config key directly
			SuccessMsg:   func(value string) string { return "Web post article size limit set to: " + value + " bytes" },
			EmptyAllowed: false,
		},
		config.FORM_FIELD_ABUSEMAIL: {
			FormField:    config.FORM_FIELD_ABUSEMAIL,
			ConfigKey:    config.CFG_KEY_ABUSEMAIL,
			Validator:    s.validateEmail,
			Processor:    nil, // Uses config key directly
			SuccessMsg:   func(value string) string { return "Abuse email set to: " + value },
			EmptyAllowed: false,
		},
		config.FORM_FIELD_WEBLOCALNNTP: {
			FormField: config.FORM_FIELD_WEBLOCALNNTP,
			ConfigKey: config.CFG_KEY_WEBLOCALNNTP,
			Validator: nil, // No validation needed
			Processor: nil, // Uses config key directly
			SuccessMsg: func(value string) string {
				if value == "" {
					return "WebLocalNNTPServerAddrInfo cleared successfully"
				}
				return "WebLocalNNTPServerAddrInfo set to: " + value
			},
			EmptyAllowed: true,
		},
		config.FORM_FIELD_REVERSEPROXY: {
			FormField: config.FORM_FIELD_REVERSEPROXY,
			ConfigKey: config.CFG_KEY_REVERSEPROXY,
			Validator: nil, // No validation needed
			Processor: nil, // Uses config key directly
			SuccessMsg: func(value string) string {
				if value == "" {
					return "Reverse proxy address cleared (reverse proxy disabled)"
				}
				return "Ok. Reboot webserver now! Reverse proxy address set to: " + value
			},
			EmptyAllowed: true,
		},
		config.FORM_FIELD_REGISTRATION: {
			FormField:    config.FORM_FIELD_REGISTRATION,
			ConfigKey:    config.CFG_KEY_REGISTRATION,
			Validator:    nil, // No validation needed
			Processor:    s.processRegistrationToggle,
			SuccessMsg:   func(value string) string { return value }, // Custom message handled in processor
			EmptyAllowed: true,                                       // Toggle doesn't need a value
		},
		config.FORM_FIELD_BLOCKBADBOTS: {
			FormField:    config.FORM_FIELD_BLOCKBADBOTS,
			ConfigKey:    config.CFG_KEY_BLOCKBADBOTS,
			Validator:    nil, // No validation needed
			Processor:    s.processBlockBadBotsToggle,
			SuccessMsg:   func(value string) string { return value }, // Custom message handled in processor
			EmptyAllowed: true,                                       // Toggle doesn't need a value
		},
		config.FORM_FIELD_BADBOTS: {
			FormField: config.FORM_FIELD_BADBOTS,
			ConfigKey: config.CFG_KEY_BADBOTS,
			Validator: nil, // Basic string validation
			Processor: s.processBadBotsUpdate, // Apply changes immediately
			SuccessMsg: func(value string) string {
				if value == "" {
					return "Bad bots list cleared and applied (using defaults)"
				}
				return "Bad bots list updated and applied immediately: " + value
			},
			EmptyAllowed: true,
		},
		config.FORM_FIELD_BLOCKBADIPS: {
			FormField:    config.FORM_FIELD_BLOCKBADIPS,
			ConfigKey:    config.CFG_KEY_BLOCKBADIPS,
			Validator:    nil, // No validation needed
			Processor:    s.processBlockBadIPsToggle,
			SuccessMsg:   func(value string) string { return value }, // Custom message handled in processor
			EmptyAllowed: true,                                       // Toggle doesn't need a value
		},
		config.FORM_FIELD_BADIPS: {
			FormField: config.FORM_FIELD_BADIPS,
			ConfigKey: config.CFG_KEY_BADIPS,
			Validator: s.validateCIDRList, // Validate CIDR ranges
			Processor: nil,                // Uses config key directly
			SuccessMsg: func(value string) string {
				if value == "" {
					return "Bad IPs list cleared (using defaults)"
				}
				return "Bad IPs list updated: " + value
			},
			EmptyAllowed: true,
		},
	}

	// Get setting configuration
	cfg, exists := settings[settingType]
	if !exists {
		session.SetError("Unknown setting type: " + settingType)
		c.Redirect(http.StatusSeeOther, "/admin?tab=settings")
		return
	}

	// Get the value from form
	value := strings.TrimSpace(c.PostForm(cfg.FormField))

	// Validate empty values
	if value == "" && !cfg.EmptyAllowed {
		switch settingType {
		case config.FORM_FIELD_WEBPOSTSIZE:
			session.SetError("Size cannot be empty")
		case config.FORM_FIELD_ABUSEMAIL:
			session.SetError("Abuse email cannot be empty")
		default:
			session.SetError("Value cannot be empty for " + settingType)
		}
		c.Redirect(http.StatusSeeOther, "/admin?tab=settings")
		return
	}

	// Run validator if provided
	if cfg.Validator != nil {
		if err := cfg.Validator(value); err != nil {
			session.SetError(err.Error())
			c.Redirect(http.StatusSeeOther, "/admin?tab=settings")
			return
		}
	}

	// Process the setting
	if settingType == config.FORM_FIELD_HOSTNAME || settingType == config.FORM_FIELD_REGISTRATION || settingType == config.FORM_FIELD_BLOCKBADBOTS {
		if err := cfg.Processor(s, value); err != nil {
			switch settingType {
			case config.FORM_FIELD_HOSTNAME:
				session.SetError("Failed to set hostname: " + err.Error())
			case config.FORM_FIELD_REGISTRATION:
				session.SetError("Failed to toggle registration: " + err.Error())
			case config.FORM_FIELD_BLOCKBADBOTS:
				session.SetError("Failed to toggle bot blocking: " + err.Error())
			}
			c.Redirect(http.StatusSeeOther, "/admin?tab=settings")
			return
		}
	} else {
		// For other settings, use the config key directly
		if err := s.DB.SetConfigValue(cfg.ConfigKey, value); err != nil {
			switch settingType {
			case config.FORM_FIELD_WEBPOSTSIZE:
				session.SetError("Failed to save configuration: " + err.Error())
			case config.FORM_FIELD_ABUSEMAIL:
				session.SetError("Failed to set abuse email: " + err.Error())
			case config.FORM_FIELD_WEBLOCALNNTP:
				session.SetError("Failed to set WebLocalNNTPServerAddrInfo: " + err.Error())
			case config.FORM_FIELD_REVERSEPROXY:
				session.SetError("Failed to set reverse proxy address: " + err.Error())
			default:
				session.SetError("Failed to update setting: " + err.Error())
			}
			c.Redirect(http.StatusSeeOther, "/admin?tab=settings")
			return
		}
	}

	// Set success message
	if settingType == config.FORM_FIELD_REGISTRATION {
		// Get the new status to show the correct message
		currentStatus, _ := s.DB.GetConfigValue(config.CFG_KEY_REGISTRATION)
		if currentStatus == "true" {
			session.SetSuccess("Registration enabled")
		} else {
			session.SetSuccess("Registration disabled")
		}
	} else if settingType == config.FORM_FIELD_BLOCKBADBOTS {
		// Get the new status to show the correct message
		currentStatus, _ := s.DB.GetConfigValue(config.CFG_KEY_BLOCKBADBOTS)
		if currentStatus == "true" {
			session.SetSuccess("Bot blocking enabled.")
		} else {
			session.SetSuccess("Bot blocking disabled.")
		}
	} else {
		session.SetSuccess(cfg.SuccessMsg(value))
	}
	c.Redirect(http.StatusSeeOther, "/admin?tab=settings")
}

// Validation functions
func (s *WebServer) validateHostname(hostname string) error {
	if hostname == "" {
		return fmt.Errorf("NNTP hostname can not be empty!")
	}
	// Just validate format here, processor will handle database operations
	return nil
}

func (s *WebServer) validateWebPostSize(sizeStr string) error {
	size, err := strconv.Atoi(sizeStr)
	if err != nil {
		return fmt.Errorf("Invalid size format: must be a number")
	}

	if size < 1024 {
		return fmt.Errorf("Size must be at least 1024 bytes (1KB)")
	}

	if size > 16*1024*1024 {
		return fmt.Errorf("Size must not exceed 16777216 bytes (16MB)")
	}

	return nil
}

func (s *WebServer) validateEmail(email string) error {
	emailRegex := regexp.MustCompile(`^[a-zA-Z0-9._%+-]+@[a-zA-Z0-9.-]+\.[a-zA-Z]{2,}$`)
	if !emailRegex.MatchString(email) {
		return fmt.Errorf("Invalid email format for abuse email")
	}
	return nil
}

// Processor functions
func (s *WebServer) processHostname(server *WebServer, hostname string) error {
	return processor.SetHostname(hostname, server.DB)
}

func (s *WebServer) processRegistrationToggle(server *WebServer, value string) error {
	// Get current registration status
	currentStatus, err := server.DB.GetConfigValue(config.CFG_KEY_REGISTRATION)
	if err != nil {
		// Default to false if not set
		currentStatus = "false"
	}

	// Toggle the status
	var newStatus string
	if currentStatus == "true" {
		newStatus = "false"
	} else {
		newStatus = "true"
	}

	// Update the configuration
	err = server.DB.SetConfigValue(config.CFG_KEY_REGISTRATION, newStatus)
	if err != nil {
		return fmt.Errorf("failed to toggle registration: %v", err)
	}

	return nil
}

func (s *WebServer) processBlockBadBotsToggle(server *WebServer, value string) error {
	// Get current bot blocking status
	currentStatus, err := server.DB.GetConfigValue(config.CFG_KEY_BLOCKBADBOTS)
	if err != nil {
		return fmt.Errorf("failed to get current BlockBadBots config: %v", err)

	}

	// Toggle the status
	var newStatus string
	if currentStatus == "true" {
		newStatus = "false"
		config.BadBotsMutex.Lock()
		config.BlockBadBots = false
		config.BadBotsMutex.Unlock()
	} else {
		newStatus = "true"
		config.BadBotsMutex.Lock()
		config.BlockBadBots = true
		config.BadBotsMutex.Unlock()
	}

	// Update the configuration
	err = server.DB.SetConfigValue(config.CFG_KEY_BLOCKBADBOTS, newStatus)
	if err != nil {
		return fmt.Errorf("failed to toggle bot blocking: %v", err)
	}

	return nil
}

// processBlockBadIPsToggle handles the IP blocking toggle
func (s *WebServer) processBlockBadIPsToggle(server *WebServer, value string) error {
	// Get current IP blocking status
	currentStatus, err := server.DB.GetConfigValue(config.CFG_KEY_BLOCKBADIPS)
	if err != nil {
		return fmt.Errorf("failed to get current BlockBadIPs config: %v", err)
	}

	// Toggle the status
	var newStatus string
	if currentStatus == "true" {
		newStatus = "false"
		config.BadIPsMutex.Lock()
		config.BlockBadIPs = false
		config.BadIPsMutex.Unlock()
	} else {
		newStatus = "true"
		config.BadIPsMutex.Lock()
		config.BlockBadIPs = true
		config.BadIPsMutex.Unlock()
	}

	// Update the configuration
	err = server.DB.SetConfigValue(config.CFG_KEY_BLOCKBADIPS, newStatus)
	if err != nil {
		return fmt.Errorf("failed to toggle IP blocking: %v", err)
	}

	return nil
}

// validateCIDRList validates a comma-separated list of CIDR ranges
func (s *WebServer) validateCIDRList(value string) error {
	if value == "" {
		return nil // Empty is allowed
	}

	// Parse comma-separated list
	for _, cidr := range strings.Split(value, ",") {
		trimmed := strings.TrimSpace(cidr)
		if trimmed == "" {
			continue // Skip empty entries
		}

		// Validate CIDR format
		_, _, err := net.ParseCIDR(trimmed)
		if err != nil {
			// Try parsing as single IP address
			ip := net.ParseIP(trimmed)
			if ip == nil {
				return fmt.Errorf("invalid IP address or CIDR range: %s", trimmed)
			}
		}
	}

	return nil
}

// processBadBotsUpdate updates bad bots configuration and applies it immediately
func (s *WebServer) processBadBotsUpdate(server *WebServer, value string) error {
	// Get current BlockBadBots setting
	blockBadBotsStr, err := server.DB.GetConfigValue(config.CFG_KEY_BLOCKBADBOTS)
	if err != nil {
		return fmt.Errorf("failed to get BlockBadBots config: %v", err)
	}
	blockEnabled := (blockBadBotsStr == "true")
	
	// Update database first
	err = server.DB.SetConfigValue(config.CFG_KEY_BADBOTS, value)
	if err != nil {
		return fmt.Errorf("failed to update BadBots config: %v", err)
	}
	
	// Apply changes immediately to global variables
	config.UpdateBadBots(value, blockEnabled)
	
	return nil
}

// processBadIPsUpdate updates bad IPs configuration and applies it immediately
func (s *WebServer) processBadIPsUpdate(server *WebServer, value string) error {
	// Get current BlockBadIPs setting
	blockBadIPsStr, err := server.DB.GetConfigValue(config.CFG_KEY_BLOCKBADIPS)
	if err != nil {
		return fmt.Errorf("failed to get BlockBadIPs config: %v", err)
	}
	blockEnabled := (blockBadIPsStr == "true")
	
	// Update database first
	err = server.DB.SetConfigValue(config.CFG_KEY_BADIPS, value)
	if err != nil {
		return fmt.Errorf("failed to update BadIPs config: %v", err)
	}
	
	// Apply changes immediately to global variables
	err = config.UpdateBadIPs(value, blockEnabled)
	if err != nil {
		return fmt.Errorf("failed to apply IP config changes: %v", err)
	}
	
	return nil
}
