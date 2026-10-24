// SPDX-License-Identifier: MIT

package session

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
)

// SessionKey identifies a FIX session independently of its TCP connection.
type SessionKey struct {
	BeginString  string
	SenderCompID string // bridge side
	TargetCompID string // client side
}

// ErrSessionActive rejects a Logon for a session that is already logged on.
var ErrSessionActive = errors.New("session already logged on")

// StoreFactory opens the store for a session the first time it logs on.
type StoreFactory func(SessionKey) (MessageStore, error)

// MemoryStoreFactory keeps session state for the life of the process only.
func MemoryStoreFactory(SessionKey) (MessageStore, error) { return NewMemoryStore(), nil }

// FileStoreFactory keeps each session's FileStore in its own directory under root.
func FileStoreFactory(root string, opts FileStoreOptions) StoreFactory {
	return func(k SessionKey) (MessageStore, error) {
		dir := strings.Join([]string{pathSafe(k.BeginString), pathSafe(k.SenderCompID), pathSafe(k.TargetCompID)}, "+")
		return OpenFileStore(filepath.Join(root, dir), opts)
	}
}

// pathSafe escapes everything but [A-Za-z0-9.-] so distinct IDs map to distinct names.
func pathSafe(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || (c == '.' && i > 0) {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "_%02X", c)
		}
	}
	return b.String()
}

// Registry hands each logged-on session its store and sequence state, keyed
// by SessionKey, so the state outlives TCP connections. At most one
// connection holds a session at a time.
type Registry struct {
	mu     sync.Mutex
	open   StoreFactory
	stores map[SessionKey]MessageStore
	active map[SessionKey]bool
}

func NewRegistry(open StoreFactory) *Registry {
	return &Registry{open: open, stores: make(map[SessionKey]MessageStore), active: make(map[SessionKey]bool)}
}

// Acquire returns the session's store, opening it on first use.
func (r *Registry) Acquire(k SessionKey) (MessageStore, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.active[k] {
		return nil, ErrSessionActive
	}
	st, ok := r.stores[k]
	if !ok {
		var err error
		if st, err = r.open(k); err != nil {
			return nil, err
		}
		r.stores[k] = st
	}
	r.active[k] = true
	return st, nil
}

// Release marks the session free for its next connection; the store stays open.
func (r *Registry) Release(k SessionKey) {
	r.mu.Lock()
	delete(r.active, k)
	r.mu.Unlock()
}

func (r *Registry) isActive(k SessionKey) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.active[k]
}

// Close closes every store.
func (r *Registry) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	var errs []error
	for k, st := range r.stores {
		errs = append(errs, st.Close())
		delete(r.stores, k)
	}
	return errors.Join(errs...)
}
