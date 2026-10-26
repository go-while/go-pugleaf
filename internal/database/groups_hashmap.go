package database

import (
	"log"
	"sync"
	"time"
)

var DefaultGroupHashExpiry = 12 * time.Hour // TODO expose to config
var DefaultCronGroupHash = 15 * time.Minute // TODO expose to config

// This GHmap is only used to map group names to hashes and vice versa.
// It is not used for any other purpose, such as storing group metadata or statuses.
// It caches the hashes of groups to avoid recalculating them
// baseGroupDBdir := filepath.Join(db.dbconfig.DataDir, "/db/"+groupsHash)

// Usage:
// hash := GroupGHmap.GroupToHash(groupName)
// group := GroupGHmap.GetGroupFromHash(hash)
// the 2nd only returns a group if it has been hashed before!

// NewGHmap returns a GHmap whose cleanup loop never stops (see NewGHmapStop).
func NewGHmap() *GHmap {
	return NewGHmapStop(nil)
}

// NewGHmapStop returns a GHmap whose cleanup loop (GHmapCron) returns when stop is
// closed. A nil stop channel never fires: the loop then runs for the life of the process.
func NewGHmapStop(stop <-chan struct{}) *GHmap {
	h := &GHmap{
		GHmap:  make(map[string]*HashedEntry, 256),
		Groups: make(map[string]*GroupEntry, 256),
		stop:   stop,
	}
	go h.GHmapCron() // Start the cleanup goroutine
	return h
}

type HashedEntry struct {
	Hash   string    // The hashed value of the group name
	Expiry time.Time // When this hash will expire
}

type GroupEntry struct {
	Group  string    // The hashed value of the group name
	Expiry time.Time // When this hash will expire
}

type GHmap struct {
	mux sync.RWMutex
	// map group name to hashed value
	GHmap     map[string]*HashedEntry // key=group value=hashed entry
	Groups    map[string]*GroupEntry  // key=hash value=group
	Hhits     uint64                  // Number of times a hash was found in the map
	Hnegs     uint64                  // Number of times a hash was not found in the map
	Ghits     uint64                  // Number of times a group was found in the map
	Gnegs     uint64                  // Number of times a group was not found in the map
	expired   uint64                  // expired since lastPrint
	lastPrint time.Time
	stop      <-chan struct{} // closed to end GHmapCron (nil: never)
}

func (h *GHmap) Ghit() {
	h.mux.Lock()
	h.Ghits++
	//log.Printf("[CACHE:GROUPHASH] Ghit! hits: %d", h.Ghits)
	h.mux.Unlock()
}

func (h *GHmap) Hhit() {
	h.mux.Lock()
	h.Hhits++
	//log.Printf("[CACHE:GROUPHASH] Hhit! hits: %d", h.Hhits)
	h.mux.Unlock()
}

func (h *GHmap) Gneg() {
	h.mux.Lock()
	h.Gnegs++
	//log.Printf("[CACHE:GROUPHASH] Gneg! negs: %d", h.Gnegs)
	h.mux.Unlock()
}

func (h *GHmap) Hneg() {
	h.mux.Lock()
	h.Hnegs++
	h.mux.Unlock()
	//log.Printf("[CACHE:GROUPHASH] Hneg! negs: %d", h.Hnegs)
}

// GHmapCron removes expired entries every DefaultCronGroupHash until h.stop is closed.
func (h *GHmap) GHmapCron() {
	for {
		select {
		case <-h.stop:
			return
		case <-time.After(DefaultCronGroupHash):
		}
		expired := 0
		// Cleanup expired entries
		now := time.Now()
		h.mux.Lock() //no defer this here we are looping!
		for group, entry := range h.GHmap {
			if now.After(entry.Expiry) {
				delete(h.GHmap, group)
			}
		}
		for hash, entry := range h.Groups {
			if now.After(entry.Expiry) {
				delete(h.Groups, hash)
				expired++
			}
		}
		if expired > 0 {
			h.expired += uint64(expired)
		}
		if time.Since(h.lastPrint) > 1*time.Hour {
			log.Printf("[CACHE:GROUPHASH] Cleaned: %d | Cached: %d [Ghits: %d, Gnegs: %d, Hhits: %d, Hnegs: %d]", h.expired, len(h.GHmap), h.Ghits, h.Gnegs, h.Hhits, h.Hnegs)
			h.expired, h.Ghits, h.Gnegs, h.Hhits, h.Hnegs = 0, 0, 0, 0, 0
			h.lastPrint = time.Now()
		}
		h.mux.Unlock()
	}
}

// GetHash retrieves the hash for a given group name, generating it if not found
func (h *GHmap) GroupToHash(group string) string {
	h.mux.RLock()
	entry, exists := h.GHmap[group]
	h.mux.RUnlock()

	if exists {
		h.Hhit()
		return entry.Hash // Return the hash string from the entry
	}

	// If not found, generate a new hash
	h.mux.Lock()
	defer h.mux.Unlock()

	// Double-check after acquiring write lock
	if entry, exists := h.GHmap[group]; exists {
		h.Hhits++
		return entry.Hash
	}

	// Generate the hash using MD5
	hashString := MD5Hash(group)
	h.GHmap[group] = &HashedEntry{Hash: hashString, Expiry: time.Now().Add(DefaultGroupHashExpiry)}
	h.Groups[hashString] = &GroupEntry{Group: group, Expiry: time.Now().Add(DefaultGroupHashExpiry)}
	h.Hnegs++
	return hashString
}

// GetGroup retrieves the group name for a given hash
func (h *GHmap) GetGroupFromHash(hash string) (string, bool) {
	h.mux.RLock()
	entry, exists := h.Groups[hash]
	h.mux.RUnlock()
	if !exists || entry == nil {
		h.Gneg()
		return "", false
	}
	h.Ghit()
	return entry.Group, true
}
