// SPDX-License-Identifier: MIT

package session

import (
	"testing"
	"time"

	"fix_bridge/codec"
)

type discHandler struct {
	recHandler
	disconnected chan bool // CancelOnDisconnect seen by OnDisconnect
}

func (h *discHandler) OnDisconnect(s *Session) { h.disconnected <- s.CancelOnDisconnect }

func codPeer(t *testing.T) (*peer, *discHandler) {
	h := &discHandler{disconnected: make(chan bool, 1)}
	p := newPeerConfigured(t, func(s *Session) { s.handler = h })
	return p, h
}

func expectDisconnect(t *testing.T, h *discHandler, wantCOD bool) {
	t.Helper()
	select {
	case cod := <-h.disconnected:
		if cod != wantCOD {
			t.Fatalf("OnDisconnect saw CancelOnDisconnect=%v, want %v", cod, wantCOD)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("OnDisconnect not called after the connection closed")
	}
}

func TestCancelOnDisconnectLogonTagAbruptClose(t *testing.T) {
	p, h := codPeer(t)
	p.logon("8013", "Y")
	_ = p.conn.Close()
	expectDisconnect(t, h, true)
}

func TestCancelOnDisconnectAfterCleanLogout(t *testing.T) {
	p, h := codPeer(t)
	p.logon("8013", "Y")
	p.send(codec.MsgTypeLogout)
	p.expect(codec.MsgTypeLogout)
	expectDisconnect(t, h, true)
}

func TestCancelOnDisconnectDefaultsOff(t *testing.T) {
	p, h := codPeer(t)
	p.logon()
	_ = p.conn.Close()
	expectDisconnect(t, h, false)
}

func TestCancelOnDisconnectInvalidValueRejectsLogon(t *testing.T) {
	p, _ := codPeer(t)
	p.sendSeq(codec.MsgTypeLogon, 1, "98", "0", "108", "30", "554", testPassword, "8013", "X")
	if m := p.expect(codec.MsgTypeLogout); m.Get(codec.TagText) != "invalid CancelOnDisconnect (8013); expected Y or N" {
		t.Fatalf("logout text %q", m.Get(codec.TagText))
	}
	p.expectClosed()
}
