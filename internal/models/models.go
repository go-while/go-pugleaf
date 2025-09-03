// Package models defines core data structures for go-pugleaf
package models

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/go-while/go-pugleaf/internal/history"
)

// Hierarchy represents a Usenet hierarchy (e.g., comp, alt, rec)
type Hierarchy struct {
	ID          int       `json:"id" db:"id"`
	Name        string    `json:"name" db:"name"`
	Description string    `json:"description" db:"description"`
	GroupCount  int       `json:"group_count" db:"group_count"`
	LastUpdated time.Time `json:"last_updated" db:"last_updated"`
	CreatedAt   time.Time `json:"created_at" db:"created_at"`
}

// Provider represents an NNTP provider configuration
type Provider struct {
	ID         int       `json:"id" db:"id"`
	Enabled    bool      `json:"enabled" db:"enabled"`   // Whether this provider is enabled
	Priority   int       `json:"priority" db:"priority"` // Priority for load balancing
	Grp        string    `json:"grp" db:"grp"`
	Name       string    `json:"name" db:"name"`
	Host       string    `json:"host" db:"host"`
	Port       int       `json:"port" db:"port"`
	SSL        bool      `json:"ssl" db:"ssl"`
	Username   string    `json:"username" db:"username"`
	Password   string    `json:"password" db:"password"`
	MaxConns   int       `json:"max_conns" db:"max_conns"`       // Maximum concurrent connections
	MaxArtSize int       `json:"max_art_size" db:"max_art_size"` // Maximum article size in bytes
	CreatedAt  time.Time `json:"created_at" db:"created_at"`
}

// Newsgroup represents a subscribed newsgroup
type Newsgroup struct {
	ID           int    `json:"id" db:"id"`
	Name         string `json:"name" db:"name"`
	Active       bool   `json:"active" db:"active"`
	Description  string `json:"description" db:"description"`
	LastArticle  int64  `json:"last_article" db:"last_article"`
	MessageCount int64  `json:"message_count" db:"message_count"`
	ExpiryDays   int    `json:"expiry_days" db:"expiry_days"`
	MaxArticles  int    `json:"max_articles" db:"max_articles"`
	MaxArtSize   int    `json:"max_art_size" db:"max_art_size"`
	Hierarchy    string `json:"hierarchy" db:"hierarchy"` // Extracted hierarchy for efficient queries
	// NNTP-specific fields
	HighWater int       `json:"high_water" db:"high_water"`
	LowWater  int       `json:"low_water" db:"low_water"`
	Status    string    `json:"status" db:"status"`
	CreatedAt time.Time `json:"created_at" db:"created_at"`
	UpdatedAt time.Time `json:"updated_at" db:"updated_at"`
}

// Overview represents XOVER data for fast group listing
// downloaded: 0 = not downloaded, 1 = downloaded
// Add 'Downloaded' field for tracking download status
// Add Sanitized flag for consistent sanitization
type Overview struct {
	ArticleNum  int64             `json:"article_num" db:"article_num"`
	Subject     string            `json:"subject" db:"subject"`
	FromHeader  string            `json:"from_header" db:"from_header"`
	DateSent    time.Time         `json:"date_sent" db:"date_sent"`
	DateString  string            `json:"date_string" db:"date_string"`
	MessageID   string            `json:"message_id" db:"message_id"`
	References  string            `json:"references" db:"references"`
	Bytes       int               `json:"bytes" db:"bytes"`
	Lines       int               `json:"lines" db:"lines"`
	ReplyCount  int               `json:"reply_count" db:"reply_count"`
	Downloaded  int               `json:"downloaded" db:"downloaded"` // 0 = not downloaded, 1 = downloaded
	Spam        int               `json:"spam" db:"spam"`             // Spam flag counter
	Hide        int               `json:"hide" db:"hide"`             // Hide flag counter
	Sanitized   bool              `json:"-" db:"-"`
	ArticleNums map[*string]int64 `json:"-" db:"-"` // key is newsgroup pointer, value is article number
}

// User represents a web user account
type User struct {
	ID               int64      `json:"id" db:"id"`
	Username         string     `json:"username" db:"username"`
	Email            string     `json:"email" db:"email"`
	PasswordHash     string     `json:"password_hash" db:"password_hash"`
	DisplayName      string     `json:"display_name" db:"display_name"`
	SessionID        string     `json:"session_id" db:"session_id"`                 // Current active session (64 chars)
	LastLoginIP      string     `json:"last_login_ip" db:"last_login_ip"`           // IP of last login (for logging only)
	SessionExpiresAt *time.Time `json:"session_expires_at" db:"session_expires_at"` // Session expiration (sliding)
	LoginAttempts    int        `json:"login_attempts" db:"login_attempts"`         // Failed login attempts counter
	CreatedAt        time.Time  `json:"created_at" db:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at" db:"updated_at"`
}

// Session represents a user session
type Session struct {
	ID        string    `json:"id" db:"id"`
	UserID    int64     `json:"user_id" db:"user_id"`
	CreatedAt time.Time `json:"created_at" db:"created_at"`
	ExpiresAt time.Time `json:"expires_at" db:"expires_at"`
}

// UserPermission represents a permission granted to a user
type UserPermission struct {
	ID         int64     `json:"id" db:"id"`
	UserID     int64     `json:"user_id" db:"user_id"`
	Permission string    `json:"permission" db:"permission"`
	GrantedAt  time.Time `json:"granted_at" db:"granted_at"`
}

// Article represents a newsgroup article (per-group DB)
type Article struct {
	GetDataFunc func(what string, group string) string `json:"-" db:"-"`
	Mux         sync.RWMutex                           `json:"-" db:"-"`
	MessageID   string                                 `json:"message_id" db:"message_id"`
	Subject     string                                 `json:"subject" db:"subject"`
	FromHeader  string                                 `json:"from_header" db:"from_header"`
	DateSent    time.Time                              `json:"date_sent" db:"date_sent"`
	DateString  string                                 `json:"date_string" db:"date_string"`
	References  string                                 `json:"references" db:"references"`
	Bytes       int                                    `json:"bytes" db:"bytes"`
	Lines       int                                    `json:"lines" db:"lines"`
	ReplyCount  int                                    `json:"reply_count" db:"reply_count"`
	HeadersJSON string                                 `json:"headers_json" db:"headers_json"`
	BodyText    string                                 `json:"body_text" db:"body_text"`
	Path        string                                 `json:"path" db:"path"` // headers network path
	ImportedAt  time.Time                              `json:"imported_at" db:"imported_at"`
	Spam        int                                    `json:"spam" db:"spam"` // Simple spam counter
	Hide        int                                    `json:"hide" db:"hide"` // Simple hide counter
	Sanitized   bool                                   `json:"-" db:"-"`
	MsgIdItem   *history.MessageIdItem                 `json:"-" db:"-"` // Cached MessageIdItem for history lookups

	// Temporary fields for parsing - not stored in database
	Headers       map[string][]string `json:"-" db:"-"` // Raw headers during parsing
	ArticleNums   map[*string]int64   `json:"-" db:"-"` // key is newsgroup pointer, value is article number
	NNTPhead      []string            `json:"-" db:"-"` // used for peering
	NNTPbody      []string            `json:"-" db:"-"` // used for peering
	IsThrRoot     bool                `json:"-" db:"-"` // used in db_batch
	IsReply       bool                `json:"-" db:"-"` // used in db_batch
	RefSlice      []string            `json:"-" db:"-"` // Parsed references for threading
	NewsgroupsPtr []*string           `json:"-" db:"-"` // Parsed newsgroup for threading
	ProcessQueue  chan *string        `json:"-" db:"-"` // newsgroup ptr for batching
}

func (a *Article) GetData(what string, group string) string {
	if a == nil {
		return ""
	}
	switch what {
	case "path":
		return a.Path
	case "references_URLs":
		if a.References == "" {
			return ""
		}
		// Split by whitespace and return the first reference as URL
		refs := strings.Fields(a.References)
		if len(refs) > 0 {
			url := fmt.Sprintf("/groups/%s/message/%s", group, refs[0]) // TODO FIXME<REVIEW, WHY FIRST AND NOT LAST?!
			return url
		}
	case "parent_URL":
		//log.Printf("DEBUG parent_URL: a.References='%s', len=%d", a.References, len(a.References))
		if a.References == "" {
			//log.Printf("DEBUG parent_URL: References is empty!")
			return ""
		}
		// Get the last (most immediate) parent reference
		refs := strings.Fields(a.References)
		//log.Printf("DEBUG parent_URL: refs=%v, len(refs)=%d", refs, len(refs))
		if len(refs) > 0 {
			parentRef := refs[len(refs)-1]
			url := fmt.Sprintf("/groups/%s/message/%s", group, parentRef)
			//log.Printf("DEBUG parent_URL: Generated URL: %s", url)
			return url
		} else {
			//log.Printf("DEBUG parent_URL: No refs found after splitting!")
		}
	}

	return ""
}

// Thread represents a parent/child relationship for threading
type Thread struct {
	ID            int    `json:"id" db:"id"`
	RootArticle   int64  `json:"root_article" db:"root_article"`
	ParentArticle *int64 `json:"parent_article" db:"parent_article"` // Pointer for NULL values
	ChildArticle  int64  `json:"child_article" db:"child_article"`
	Depth         int    `json:"depth" db:"depth"`
	ThreadOrder   int    `json:"thread_order" db:"thread_order"`
}

// ForumThread represents a complete thread with root article and replies
type ForumThread struct {
	RootArticle  *Overview   `json:"thread_root"`   // The original post
	Replies      []*Overview `json:"replies"`       // All replies in flat list
	MessageCount int         `json:"message_count"` // Total messages in thread
	LastActivity time.Time   `json:"last_activity"` // Most recent activity
}

func (f *ForumThread) PrintLastActivity() string {
	// This function returns a human-readable time difference from now
	if f.LastActivity.IsZero() {
		return "never"
	}

	diff := time.Since(f.LastActivity)
	totalDays := int(diff.Hours() / 24)

	if diff < time.Minute {
		return fmt.Sprintf("%d seconds ago", int(diff.Seconds()))
	} else if diff < time.Hour {
		return fmt.Sprintf("%d minutes ago", int(diff.Minutes()))
	} else if diff < 24*time.Hour {
		return fmt.Sprintf("%d hours ago", int(diff.Hours()))
	} else if totalDays < 30 {
		return fmt.Sprintf("%d days ago", totalDays)
	} else if totalDays < 365 {
		months := totalDays / 30
		remainingDays := totalDays % 30
		if remainingDays > 0 {
			if months == 1 {
				return fmt.Sprintf("1 Month %d Days ago", remainingDays)
			}
			return fmt.Sprintf("%d Months %d Days ago", months, remainingDays)
		} else {
			if months == 1 {
				return "1 Month ago"
			}
			return fmt.Sprintf("%d Months ago", months)
		}
	} else {
		years := totalDays / 365
		remainingDays := totalDays % 365
		months := remainingDays / 30
		if months > 0 {
			if years == 1 && months == 1 {
				return "1 Year 1 Month ago"
			} else if years == 1 {
				return fmt.Sprintf("1 Year %d Months ago", months)
			} else if months == 1 {
				return fmt.Sprintf("%d Years 1 Month ago", years)
			} else {
				return fmt.Sprintf("%d Years %d Months ago", years, months)
			}
		} else {
			if years == 1 {
				return "1 Year ago"
			}
			return fmt.Sprintf("%d Years ago", years)
		}
	}
}

// PrintLastActivity returns a human-readable time difference from now for newsgroups
func (n *Newsgroup) PrintLastActivity() string {
	// This function returns a human-readable time difference from now
	if n.UpdatedAt.IsZero() {
		return "never"
	}

	// Ensure we're working with UTC times for consistent calculation
	now := time.Now().UTC()
	updatedAtUTC := n.UpdatedAt.UTC()
	diff := now.Sub(updatedAtUTC)
	totalDays := int(diff.Hours() / 24)

	// Debug: log the actual timestamp and calculated diff
	/*
		log.Printf("DEBUG models.go PrintLastActivity: Group=%s, UpdatedAt=%v (UTC: %v), Now=%v, Diff=%v, Hours=%.1f",
			n.Name, n.UpdatedAt, updatedAtUTC, now, diff, diff.Hours())
	*/
	if diff < time.Minute {
		return fmt.Sprintf("%d seconds ago", int(diff.Seconds()))
	} else if diff < time.Hour {
		return fmt.Sprintf("%d minutes ago", int(diff.Minutes()))
	} else if diff < 48*time.Hour {
		return fmt.Sprintf("%d hours ago", int(diff.Hours()))
	} else if totalDays < 365 {
		/*
			months := totalDays / 30
			remainingDays := totalDays % 30
			if remainingDays > 0 {
				if months == 1 {
					return fmt.Sprintf("1 Month %d Days ago", remainingDays)
				}
				return fmt.Sprintf("%d Months %d Days ago", months, remainingDays)
			} else {
				if months == 1 {
					return "1 Month ago"
				}
				return fmt.Sprintf("%d Months ago", months)
			}
		*/
		return fmt.Sprintf("%d days ago", totalDays)
	} else {
		years := totalDays / 365
		remainingDays := totalDays % 365
		months := remainingDays / 30
		if months > 0 {
			if years == 1 && months == 1 {
				return "1 Year 1 Month ago"
			} else if years == 1 {
				return fmt.Sprintf("1 Year %d Months ago", months)
			} else if months == 1 {
				return fmt.Sprintf("%d Years 1 Month ago", years)
			} else {
				return fmt.Sprintf("%d Years %d Months ago", years, months)
			}
		} else {
			if years == 1 {
				return "1 Year ago"
			}
			return fmt.Sprintf("%d Years ago", years)
		}
	}
}

// PaginatedResponse represents a paginated API response
type PaginatedResponse struct {
	Data       interface{} `json:"data"`
	Page       int         `json:"page"`
	PageSize   int         `json:"page_size"`
	TotalCount int         `json:"total_count"`
	TotalPages int         `json:"total_pages"`
	HasNext    bool        `json:"has_next"`
	HasPrev    bool        `json:"has_prev"`
}

// PaginationInfo represents pagination information for templates
type PaginationInfo struct {
	CurrentPage int
	PageSize    int
	TotalCount  int
	TotalPages  int
	HasNext     bool
	HasPrev     bool
	NextPage    int
	PrevPage    int
}

// NewPaginationInfo creates pagination info
func NewPaginationInfo(page, pageSize, totalCount int) *PaginationInfo {
	totalPages := (totalCount + pageSize - 1) / pageSize
	if totalPages == 0 {
		totalPages = 1
	}

	return &PaginationInfo{
		CurrentPage: page,
		PageSize:    pageSize,
		TotalCount:  totalCount,
		TotalPages:  totalPages,
		HasNext:     page < totalPages,
		HasPrev:     page > 1,
		NextPage:    page + 1,
		PrevPage:    page - 1,
	}
}

// ReferenceCount returns the number of references in the References field
// This represents how many articles this one is replying to, not how many replies it has
func (o *Overview) ReferenceCount() int {
	if o.References == "" {
		return 0
	}

	// Split by whitespace and count non-empty Message-IDs
	refs := strings.Fields(o.References)
	return len(refs)
}

// Section represents a RockSolid Light section (from menu.conf)
type Section struct {
	ID               int       `json:"id" db:"id"`
	Name             string    `json:"name" db:"name"`
	DisplayName      string    `json:"display_name" db:"display_name"`
	Description      string    `json:"description" db:"description"`
	ShowInHeader     bool      `json:"show_in_header" db:"show_in_header"`
	EnableLocalSpool bool      `json:"enable_local_spool" db:"enable_local_spool"`
	SortOrder        int       `json:"sort_order" db:"sort_order"`
	CreatedAt        time.Time `json:"created_at" db:"created_at"`
	GroupCount       int       `json:"group_count" db:"group_count"` // Number of newsgroups assigned to this section
}

// SectionGroup represents a newsgroup assigned to a section
type SectionGroup struct {
	ID               int       `json:"id" db:"id"`
	SectionID        int       `json:"section_id" db:"section_id"`
	NewsgroupName    string    `json:"newsgroup_name" db:"newsgroup_name"`
	GroupDescription string    `json:"group_description" db:"group_description"`
	SortOrder        int       `json:"sort_order" db:"sort_order"`
	IsCategoryHeader bool      `json:"is_category_header" db:"is_category_header"`
	CreatedAt        time.Time `json:"created_at" db:"created_at"`
}

// ActiveNewsgroup represents a newsgroup in the active.db
type ActiveNewsgroup struct {
	GroupID      int       `db:"group_id" json:"group_id"`
	GroupName    string    `db:"group_name" json:"group_name"`
	Description  string    `db:"description" json:"description"`
	HighWater    int       `db:"high_water" json:"high_water"`
	LowWater     int       `db:"low_water" json:"low_water"`
	MessageCount int       `db:"message_count" json:"message_count"`
	Status       string    `db:"status" json:"status"`
	CreatedAt    time.Time `db:"created_at" json:"created_at"`
	UpdatedAt    time.Time `db:"updated_at" json:"updated_at"`
}

// GroupStatus constants for ActiveNewsgroup
const (
	StatusActive    = "y" // Active - posting allowed
	StatusNoPost    = "n" // No posting allowed
	StatusModerated = "m" // Moderated
	StatusDisabled  = "x" // Disabled
)

type APIToken struct {
	ID         int       `json:"id" db:"id"`
	APIToken   string    `json:"apitoken" db:"apitoken"`   // Unique token string
	OwnerName  string    `json:"ownername" db:"ownername"` // Name of the owner (system or user)
	OwnerID    int       `json:"ownerid" db:"ownerid"`     // 0 for system tokens, >0 for user IDs
	CreatedAt  time.Time `json:"created_at" db:"created_at"`
	LastUsedAt time.Time `json:"last_used_at" db:"last_used_at"`
	ExpiresAt  time.Time `json:"expires_at" db:"expires_at"`
	IsEnabled  bool      `json:"is_enabled" db:"is_enabled"`   // Administrative control
	UsageCount int       `json:"usage_count" db:"usage_count"` // Number of times used
}

// AIModel represents an AI model configuration for chat functionality
type AIModel struct {
	ID              int       `json:"id" db:"id"`
	PostKey         string    `json:"post_key" db:"post_key"`                   // Key used in frontend forms (e.g. "gemma3_12b")
	OllamaModelName string    `json:"ollama_model_name" db:"ollama_model_name"` // Real Ollama model name (e.g. "gemma3:12b")
	DisplayName     string    `json:"display_name" db:"display_name"`           // User-friendly name (e.g. "Gemma 3 12B")
	Description     string    `json:"description" db:"description"`             // Short description for users
	IsActive        bool      `json:"is_active" db:"is_active"`                 // Admin can enable/disable
	IsDefault       bool      `json:"is_default" db:"is_default"`               // Default selection for new chats
	SortOrder       int       `json:"sort_order" db:"sort_order"`               // Display order in UI
	CreatedAt       time.Time `json:"created_at" db:"created_at"`
	UpdatedAt       time.Time `json:"updated_at" db:"updated_at"`
}

// NNTPUser represents an NNTP-specific user account for newsreader clients
type NNTPUser struct {
	ID        int        `json:"id" db:"id"`
	Username  string     `json:"username" db:"username"`
	Password  string     `json:"password" db:"password"` // Plain text or hashed, depending on implementation
	MaxConns  int        `json:"maxconns" db:"maxconns"`
	Posting   bool       `json:"posting" db:"posting"`
	WebUserID int64      `json:"web_user_id" db:"web_user_id"` // Optional mapping to web user (0 = no mapping)
	CreatedAt time.Time  `json:"created_at" db:"created_at"`
	UpdatedAt time.Time  `json:"updated_at" db:"updated_at"`
	LastLogin *time.Time `json:"last_login" db:"last_login"`
	IsActive  bool       `json:"is_active" db:"is_active"`
}

// NNTPSession represents an active NNTP connection session
type NNTPSession struct {
	ID           int       `json:"id" db:"id"`
	UserID       int       `json:"user_id" db:"user_id"`
	ConnectionID string    `json:"connection_id" db:"connection_id"`
	RemoteAddr   string    `json:"remote_addr" db:"remote_addr"`
	StartedAt    time.Time `json:"started_at" db:"started_at"`
	LastActivity time.Time `json:"last_activity" db:"last_activity"`
	IsActive     bool      `json:"is_active" db:"is_active"`
}

// Setting represents a system configuration setting
type Setting struct {
	Key   string `json:"key" db:"key"`
	Value string `json:"value" db:"value"`
}

// SiteNews represents a site news item
type SiteNews struct {
	ID            int       `json:"id" db:"id"`
	Subject       string    `json:"subject" db:"subject"`
	Content       string    `json:"content" db:"content"`
	DatePublished time.Time `json:"date_published" db:"date_published"`
	IsVisible     bool      `json:"is_visible" db:"is_visible"`
	CreatedAt     time.Time `json:"created_at" db:"created_at"`
	UpdatedAt     time.Time `json:"updated_at" db:"updated_at"`
}

// SpamTracking represents spam tracking for an article in the main database
type SpamTracking struct {
	ID          int   `json:"id" db:"id"`
	NewsgroupID int   `json:"newsgroup_id" db:"newsgroup_id"`
	ArticleNum  int64 `json:"article_num" db:"article_num"`
}
