// SPDX-License-Identifier: MIT

package session

import "sync"

// MessageStore keeps sent frames by MsgSeqNum so a ResendRequest can be
// served, plus the session's next inbound and outbound MsgSeqNum.
type MessageStore interface {
	// Save records a frame before it is sent; the next outbound MsgSeqNum
	// becomes seq+1. An error means it is not durable and must not be sent.
	Save(seq int, frame []byte) error
	// Get returns a stored frame; false if unknown or past retention.
	Get(seq int) ([]byte, bool)
	// SetNextInSeq records the next expected inbound MsgSeqNum.
	SetNextInSeq(n int) error
	// SeqNums returns the next expected inbound and next outbound MsgSeqNum.
	SeqNums() (nextIn, nextOut int)
	// Reset drops all messages and sets both sequence numbers back to 1.
	Reset()
	Close() error
}

// MemoryStore is an in-process MessageStore; contents are lost on restart.
type MemoryStore struct {
	mu      sync.Mutex
	msgs    map[int][]byte
	nextIn  int
	nextOut int
	epoch   int64
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{msgs: make(map[int][]byte), nextIn: 1, nextOut: 1}
}

func (m *MemoryStore) Save(seq int, frame []byte) error {
	m.mu.Lock()
	m.msgs[seq] = frame
	m.nextOut = seq + 1
	m.mu.Unlock()
	return nil
}

func (m *MemoryStore) Get(seq int) ([]byte, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	f, ok := m.msgs[seq]
	return f, ok
}

func (m *MemoryStore) SetNextInSeq(n int) error {
	m.mu.Lock()
	m.nextIn = n
	m.mu.Unlock()
	return nil
}

func (m *MemoryStore) SeqNums() (int, int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.nextIn, m.nextOut
}

func (m *MemoryStore) Reset() {
	m.mu.Lock()
	m.msgs = make(map[int][]byte)
	m.nextIn, m.nextOut = 1, 1
	m.mu.Unlock()
}

func (m *MemoryStore) Close() error { return nil }
