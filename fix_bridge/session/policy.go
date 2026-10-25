// SPDX-License-Identifier: MIT
package session

import (
	"fix_bridge/codec"
	"fmt"
	"os"
	"strconv"
	"time"
)

// ConfigurePolicy leaves existing deployment behavior unchanged unless requested.
// Explicit booleans override the convenience preset.
func (s *Session) ConfigurePolicy() error {
	preset := os.Getenv("FXP_FIX_COMPAT")
	if preset != "" && preset != "default" && preset != "quickfix" {
		return fmt.Errorf("FXP_FIX_COMPAT must be default or quickfix")
	}
	s.QuickFIXCompatible = preset == "quickfix"
	s.SilentLogonFailures = preset == "quickfix"
	s.WaitPeerLogout = preset == "quickfix"
	for key, dst := range map[string]*bool{"FXP_FIX_SILENT_LOGON_FAILURES": &s.SilentLogonFailures, "FXP_FIX_WAIT_PEER_LOGOUT": &s.WaitPeerLogout} {
		if value := os.Getenv(key); value != "" {
			b, err := strconv.ParseBool(value)
			if err != nil {
				return fmt.Errorf("%s must be a boolean", key)
			}
			*dst = b
		}
	}
	return nil
}

// Fatal errors never resume application dispatch. The optional wait reads only
// for the authenticated peer's Logout and is bounded by the existing timeout.
func (s *Session) finishFatal(reason string, msg *codec.Message) {
	s.sendLogout(reason, msg)
	if !s.WaitPeerLogout {
		return
	}
	timeout := s.LogoutTimeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	if s.conn.SetReadDeadline(time.Now().Add(timeout)) != nil {
		return
	}
	s.flush(flushTimeout)
	for {
		next, err := s.parser.ReadMessage()
		if err != nil {
			return
		}
		if next.MsgType() == codec.MsgTypeLogout && next.Get(codec.TagSenderCompID) == s.target() && next.Get(codec.TagTargetCompID) == s.SenderCompID {
			_ = s.handleLogout(next)
			return
		}
	}
}

func (s *Session) sendPossDupReject(msg *codec.Message, seq int, reason, text string) {
	tag := 0
	if reason == codec.RejectRequiredTagMissing || reason == codec.RejectIncorrectDataFormat {
		tag = codec.TagOrigSendingTime
	}
	s.sendRejectRef(seq, tag, msg.MsgType(), reason, text)
}
