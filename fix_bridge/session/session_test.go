package session

import (
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
