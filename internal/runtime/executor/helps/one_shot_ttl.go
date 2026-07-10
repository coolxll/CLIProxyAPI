package helps

import (
	"sync"
	"time"
)

// OneShotTTLSet stores bounded expiring keys that are removed on first use.
type OneShotTTLSet struct {
	mu         sync.Mutex
	entries    map[string]time.Time
	maxEntries int
}

// NewOneShotTTLSet creates a bounded one-shot TTL set.
func NewOneShotTTLSet(maxEntries int) *OneShotTTLSet {
	if maxEntries <= 0 {
		maxEntries = 1
	}
	return &OneShotTTLSet{
		entries:    make(map[string]time.Time),
		maxEntries: maxEntries,
	}
}

// Mark stores key until ttl elapses. Empty keys and non-positive TTLs are ignored.
func (s *OneShotTTLSet) Mark(key string, ttl time.Duration) bool {
	if s == nil || key == "" || ttl <= 0 {
		return false
	}
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deleteExpiredLocked(now)
	if _, exists := s.entries[key]; !exists && len(s.entries) >= s.maxEntries {
		s.deleteOldestLocked()
	}
	s.entries[key] = now.Add(ttl)
	return true
}

// Consume removes and reports an unexpired key.
func (s *OneShotTTLSet) Consume(key string) bool {
	if s == nil || key == "" {
		return false
	}
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	expiresAt, exists := s.entries[key]
	if !exists {
		return false
	}
	delete(s.entries, key)
	return expiresAt.After(now)
}

func (s *OneShotTTLSet) deleteExpiredLocked(now time.Time) {
	for key, expiresAt := range s.entries {
		if !expiresAt.After(now) {
			delete(s.entries, key)
		}
	}
}

func (s *OneShotTTLSet) deleteOldestLocked() {
	oldestKey := ""
	var oldestExpiry time.Time
	for key, expiresAt := range s.entries {
		if oldestKey == "" || expiresAt.Before(oldestExpiry) {
			oldestKey = key
			oldestExpiry = expiresAt
		}
	}
	if oldestKey != "" {
		delete(s.entries, oldestKey)
	}
}
