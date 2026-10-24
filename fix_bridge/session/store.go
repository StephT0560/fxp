package session

import "sync"

// MessageStore keeps sent frames by MsgSeqNum so a ResendRequest can be served.
type MessageStore interface {
	Save(seq int, frame []byte)
	Get(seq int) ([]byte, bool)
	Reset()
}

// MemoryStore is an in-process MessageStore; contents are lost on restart.
type MemoryStore struct {
	mu   sync.Mutex
	msgs map[int][]byte
}

func NewMemoryStore() *MemoryStore { return &MemoryStore{msgs: make(map[int][]byte)} }

func (m *MemoryStore) Save(seq int, frame []byte) {
	m.mu.Lock()
	m.msgs[seq] = frame
	m.mu.Unlock()
}

func (m *MemoryStore) Get(seq int) ([]byte, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	f, ok := m.msgs[seq]
	return f, ok
}

func (m *MemoryStore) Reset() {
	m.mu.Lock()
	m.msgs = make(map[int][]byte)
	m.mu.Unlock()
}
