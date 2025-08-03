package database

import (
	"log"
	"sync"
	"time"
)

var DefaultGroupSectionsCacheExpiry = 60 * time.Second
var CronGroupSectionsCache = 15 * time.Second

// GroupSectionDBCache only caches if a group is in legacy sections
type GroupSectionDBCache struct {
	mux           sync.RWMutex
	SectionsCache map[string]time.Time // (key: group name, value: expiry time)
}

func NewGroupSectionDBCache() *GroupSectionDBCache {
	cache := &GroupSectionDBCache{
		SectionsCache: make(map[string]time.Time, 256),
	}
	go cache.CronClean() // Start the cleanup goroutine
	return cache
}

func (g *GroupSectionDBCache) IsInSections(group string) bool {
	g.mux.RLock()
	defer g.mux.RUnlock()
	if g.SectionsCache == nil {
		return false
	}
	_, exists := g.SectionsCache[group]
	if exists {
		//log.Printf("Group %s found in sections cache", group)
		return true
	}
	//log.Printf("Group %s not found in sections cache", group)
	return false
}

func (g *GroupSectionDBCache) CronClean() {
	for {
		time.Sleep(CronGroupSectionsCache)

		g.mux.RLock()
		if g.SectionsCache == nil {
			g.mux.RUnlock()
			continue
		}
		if len(g.SectionsCache) == 0 {
			g.mux.RUnlock()
			continue
		}
		g.mux.RUnlock()

		now := time.Now()
		expired := 0
		g.mux.Lock()
		for group, expiry := range g.SectionsCache {
			if now.After(expiry) {
				log.Printf("[CACHE:SECTIONS] expired '%s'", group)
				delete(g.SectionsCache, group)
				expired++
			}
		}
		if expired > 0 {
			log.Printf("[CACHE:SECTIONS] Cleaned: %d, Cached: %d", expired, len(g.SectionsCache))
		}
		g.mux.Unlock()
	}
}

func (g *GroupSectionDBCache) AddGroupToSectionsCache(group string) {
	g.mux.Lock()
	if g.SectionsCache == nil {
		g.SectionsCache = make(map[string]time.Time)
	}
	if _, exists := g.SectionsCache[group]; !exists {
		g.SectionsCache[group] = time.Now().Add(DefaultGroupSectionsCacheExpiry)
		//log.Printf("Added to sections cache: group %s ", group)
	}
	g.mux.Unlock()
}
