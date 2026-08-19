package runtime

import (
	"sync"
	"time"
)

type OperationGrant struct {
	Allowed   bool      `json:"allowed"`
	ExpiresAt time.Time `json:"expiresAt"`
}

func (g OperationGrant) Valid(now time.Time) bool {
	return g.Allowed && !g.ExpiresAt.IsZero() && now.Before(g.ExpiresAt)
}

type OperationGrantStore interface {
	Current() OperationGrant
	Update(OperationGrant)
}

type MemoryOperationGrantStore struct {
	mu    sync.RWMutex
	grant OperationGrant
}

func NewMemoryOperationGrantStore() *MemoryOperationGrantStore {
	return &MemoryOperationGrantStore{}
}

func (s *MemoryOperationGrantStore) Current() OperationGrant {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.grant
}

func (s *MemoryOperationGrantStore) Update(grant OperationGrant) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.grant = grant
}
