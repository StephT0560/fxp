// SPDX-License-Identifier: MIT
package session

import (
	"fix_bridge/codec"
	"net"
	"testing"
	"time"
)

func TestR4ReverseRouteDoesNotLeakIntoNextReply(t *testing.T) {
	p := newPeer(t)
	p.logon()
	b := codec.NewBuilder("1", p.sender, p.target, 2)
	for tag, value := range map[int]string{115: "ORIGIN", 116: "SUB", 144: "LOC", 128: "DEST", 129: "DSUB", 145: "DLOC", 50: "SOURCE-SUB", 57: "TARGET-SUB", 142: "SOURCE-LOC", 143: "TARGET-LOC"} {
		b.Set(tag, value)
	}
	b.Set(112, "route")
	p.write(b.Build())
	m := p.expect("0")
	for tag, value := range map[int]string{128: "ORIGIN", 129: "SUB", 145: "LOC", 115: "DEST", 116: "DSUB", 144: "DLOC", 57: "SOURCE-SUB", 50: "TARGET-SUB", 143: "SOURCE-LOC", 142: "TARGET-LOC"} {
		if m.Get(tag) != value {
			t.Fatalf("route tag %d=%q", tag, m.Get(tag))
		}
	}
	p.sendSeq("1", 3, "112", "clean")
	m = p.expect("0")
	if m.Get(128) != "" {
		t.Fatal("route leaked")
	}
}
func TestR4DrainBufferedAdminAfterRejectedReplay(t *testing.T) {
	p := newPeer(t)
	p.logon()
	p.sendSeq("1", 3, "112", "FIRST")
	p.expect("2")
	p.sendSeq("D", 2, "43", "Y", "122", fixTime(time.Now()), "126", "bad")
	p.expect("3")
	if m := p.expect("0"); m.Get(112) != "FIRST" {
		t.Fatal("buffered admin lost")
	}
	p.sendSeq("1", 4, "112", "SECOND")
	if m := p.expect("0"); m.Get(112) != "SECOND" {
		t.Fatal("incorrect order")
	}
}
func TestR4LowResendIsIdempotentAndDoesNotAdvanceInput(t *testing.T) {
	p := newPeer(t)
	p.logon()
	p.sendSeq("1", 2, "112", "H")
	p.expect("0")
	p.sendSeq("2", 3, "7", "1", "16", "0")
	p.expect("4")
	p.sendSeq("2", 3, "7", "1", "16", "0")
	p.expect("4")
	p.sendSeq("1", 4, "112", "NEXT")
	p.expect("0")
}

func TestR4HeartbeatStartsWithNegotiatedInterval(t *testing.T) {
	fastHeartbeats(t)
	p := newPeer(t)
	p.logon("108", "1")
	select {
	case msg := <-p.in:
		if msg.MsgType() != "0" {
			t.Fatalf("first idle message=%s", msg.MsgType())
		}
	case <-time.After(70 * time.Millisecond):
		t.Fatal("monitor slept using pre-Logon default instead of negotiated interval")
	}
}

func TestR4ApplicationGapIsDrainedOnceAfterRecovery(t *testing.T) {
	p := newPeer(t)
	p.logon()
	p.sendSeq("D", 3, "11", "buffered")
	p.expect("2")
	p.sendSeq("D", 2, "11", "replayed")
	waitFor(t, "buffered order delivered", func() bool { return p.h.appCount() == 2 })
	p.sendSeq("D", 3, "11", "buffered", "43", "Y", "122", fixTime(time.Now()))
	p.expectQuiet(20 * time.Millisecond)
	if p.h.appCount() != 2 {
		t.Fatal("replayed duplicate executed twice")
	}
}

func TestR4PossDupRejectHasReferences(t *testing.T) {
	p := newPeer(t)
	p.logon()
	p.sendSeq("1", 2, "112", "seen")
	p.expect("0")
	p.sendSeq("D", 2, "43", "Y")
	m := p.expect("3")
	if m.Get(371) != "122" || m.Get(372) != "D" {
		t.Fatal("PossDup reject missing field/type references")
	}
}

func TestR4GapBufferIsBounded(t *testing.T) {
	p := newPeer(t)
	p.logon()
	p.sendSeq("1", 3, "112", "gap")
	p.expect("2")
	for seq := 4; seq <= 67; seq++ {
		p.sendSeq("1", seq, "112", "gap")
	}
	p.expect("5")
	p.expectClosed()
}
func TestR4ResetDropsBufferedOldSessionMessages(t *testing.T) {
	p := newPeer(t)
	p.logon()
	p.sendSeq("D", 3, "11", "old")
	p.expect("2")
	p.sendSeq("A", 1, "98", "0", "108", "30", "141", "Y")
	p.expect("A")
	p.sendSeq("D", 2, "11", "new")
	waitFor(t, "new order after reset", func() bool { return p.h.appCount() >= 1 })
	p.expectQuiet(30 * time.Millisecond)
	if p.h.appCount() != 1 {
		t.Fatal("old buffered application survived explicit session reset")
	}
}

func TestR4PossDupAccuracyFailureIsFatal(t *testing.T) {
	p := newPeer(t)
	p.logon()
	p.sendSeq("1", 2, "112", "seen")
	p.expect("0")
	p.sendSeq("D", 2, "43", "Y", "122", fixTime(time.Now().Add(time.Minute)))
	p.expect("3")
	p.expect("5")
	p.expectClosed()
}

type blockedCloseConn struct {
	net.Conn
	release <-chan struct{}
}

func (c blockedCloseConn) Close() error { err := c.Conn.Close(); <-c.release; return err }
func TestR4FixtureStoreReleasedBeforePeerSeesClose(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	registry := NewRegistry(MemoryStoreFactory)
	p := newPeerConfigured(t, func(s *Session) { s.Registry = registry; s.conn = blockedCloseConn{s.conn, release} })
	p.logon()
	p.sendSeq("5", 2)
	p.expect("5")
	p.expectClosed()
	if registry.isActive(SessionKey{"FIX.4.4", "FXPBRIDGE", "CLIENT"}) {
		t.Fatal("closed echo session still owns registry slot")
	}
}
