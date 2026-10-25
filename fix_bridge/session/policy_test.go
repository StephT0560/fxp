// SPDX-License-Identifier: MIT
package session

import (
	"testing"
	"time"
)

func TestQuickfixPolicyPresetAndOverrides(t *testing.T) {
	t.Setenv("FXP_FIX_COMPAT", "")
	t.Setenv("FXP_FIX_SILENT_LOGON_FAILURES", "")
	t.Setenv("FXP_FIX_WAIT_PEER_LOGOUT", "")
	s := NewSession(nil, "", nil)
	if err := s.ConfigurePolicy(); err != nil {
		t.Fatal(err)
	}
	if s.SilentLogonFailures || s.WaitPeerLogout {
		t.Fatal("defaults changed")
	}
	t.Setenv("FXP_FIX_COMPAT", "quickfix")
	if err := s.ConfigurePolicy(); err != nil {
		t.Fatal(err)
	}
	if !s.SilentLogonFailures || !s.WaitPeerLogout {
		t.Fatal("preset not applied")
	}
	t.Setenv("FXP_FIX_WAIT_PEER_LOGOUT", "false")
	if err := s.ConfigurePolicy(); err != nil {
		t.Fatal(err)
	}
	if s.WaitPeerLogout {
		t.Fatal("explicit override ignored")
	}
	t.Setenv("FXP_FIX_COMPAT", "unknown")
	if s.ConfigurePolicy() == nil {
		t.Fatal("bad preset accepted")
	}
}
func TestStrictFatalWaitsForPeerOrTimeout(t *testing.T) {
	p := newPeerConfigured(t, func(s *Session) { s.WaitPeerLogout = true; s.LogoutTimeout = 80 * time.Millisecond })
	p.logon()
	p.sendSeq("1", 2, "49", "WRONG", "112", "bad")
	p.expect("3")
	p.expect("5")
	select {
	case <-p.closed:
		t.Fatal("closed before peer Logout")
	case <-time.After(10 * time.Millisecond):
	}
	p.sendSeq("D", 3, "11", "must-not-execute")
	p.sendSeq("5", 4)
	select {
	case <-p.closed:
		if p.h.appCount() != 0 {
			t.Fatal("application dispatched after fatal error")
		}
	case <-time.After(time.Second):
		t.Fatal("peer Logout did not close")
	}
}

func TestStrictFatalWaitIsBounded(t *testing.T) {
	p := newPeerConfigured(t, func(s *Session) { s.WaitPeerLogout = true; s.LogoutTimeout = 30 * time.Millisecond })
	p.logon()
	p.sendSeq("1", 2, "49", "WRONG", "112", "bad")
	p.expect("3")
	p.expect("5")
	p.expectClosed()
}
