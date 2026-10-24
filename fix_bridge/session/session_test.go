// SPDX-License-Identifier: MIT

package session

import (
	"errors"
	"net"
	"testing"
	"time"
)

func TestSinceLastSentReadsTimestamp(t *testing.T) {
	srv, cli := net.Pipe()
	defer srv.Close()
	defer cli.Close()
	s := NewSession(srv, "FXPBRIDGE", nil)
	s.lastSent = time.Now().Add(-time.Minute)
	if d := s.sinceLastSent(); d < time.Minute || d > 2*time.Minute {
		t.Fatalf("sinceLastSent = %v, want ~1m", d)
	}
}

// A client that stops reading must not wedge SendWithSeq (and every caller
// behind sendMu) forever: the session is closed after a bounded wait instead.
func TestSendWithSeqBackpressureClosesSession(t *testing.T) {
	srv, cli := net.Pipe() // cli never reads, so writeLoop blocks
	defer cli.Close()
	old := sendEnqueueTimeout
	sendEnqueueTimeout = 50 * time.Millisecond
	defer func() { sendEnqueueTimeout = old }()

	s := NewSession(srv, "FXPBRIDGE", nil)
	s.sendCh = make(chan []byte, 1)
	go s.writeLoop()

	finished := make(chan struct{})
	go func() {
		for i := 0; i < 4; i++ {
			s.SendWithSeq(func(seq int) []byte { return []byte("x") })
		}
		close(finished)
	}()
	select {
	case <-finished:
	case <-time.After(2 * time.Second):
		t.Fatal("SendWithSeq blocked indefinitely on a stalled client")
	}
	select {
	case <-s.done:
	default:
		t.Fatal("session not closed after send backpressure timeout")
	}
}

func TestStalledSessionDoesNotBlockHealthySession(t *testing.T) {
	stalledConn, stalledPeer := net.Pipe()
	healthyConn, healthyPeer := net.Pipe()
	stalled := NewSession(stalledConn, "FXPBRIDGE", nil)
	healthy := NewSession(healthyConn, "FXPBRIDGE", nil)
	go stalled.writeLoop()
	go healthy.writeLoop()
	defer func() {
		_ = stalledPeer.Close()
		_ = healthyPeer.Close()
		_ = stalledConn.Close()
		_ = healthyConn.Close()
	}()

	stalled.SendWithSeq(func(int) []byte { return []byte("stalled") })
	healthy.SendWithSeq(func(int) []byte { return []byte("healthy") })
	if err := healthyPeer.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len("healthy"))
	if _, err := healthyPeer.Read(got); err != nil {
		t.Fatalf("healthy session output blocked behind stalled session: %v", err)
	}
	if string(got) != "healthy" {
		t.Fatalf("healthy session output = %q", got)
	}
}

type failingStore struct {
	*MemoryStore
	failSave, failIn bool
}

func (f *failingStore) Save(seq int, frame []byte) error {
	if f.failSave {
		return errors.New("disk full")
	}
	return f.MemoryStore.Save(seq, frame)
}

func (f *failingStore) SetNextInSeq(n int) error {
	if f.failIn {
		return errors.New("disk full")
	}
	return f.MemoryStore.SetNextInSeq(n)
}

func TestStoreSaveFailureClosesSessionWithoutSending(t *testing.T) {
	srv, cli := net.Pipe()
	defer func() { _ = cli.Close() }()
	s := NewSession(srv, "FXPBRIDGE", nil)
	s.Store = &failingStore{MemoryStore: NewMemoryStore(), failSave: true}
	s.SendWithSeq(func(int) []byte { return []byte("x") })
	select {
	case <-s.done:
	default:
		t.Fatal("session not closed after store write failure")
	}
	if n := len(s.sendCh); n != 0 {
		t.Fatalf("%d unpersisted frame(s) queued for the wire", n)
	}
}

func TestStoreInSeqFailureClosesSession(t *testing.T) {
	srv, cli := net.Pipe()
	defer func() { _ = cli.Close() }()
	s := NewSession(srv, "FXPBRIDGE", nil)
	s.Store = &failingStore{MemoryStore: NewMemoryStore(), failIn: true}
	if err := s.setInSeq(2); err == nil {
		t.Fatal("setInSeq must report the store failure")
	}
	select {
	case <-s.done:
	default:
		t.Fatal("session not closed after store write failure")
	}
}
