//go:build quickfix_fixture

// SPDX-License-Identifier: MIT
package main

import (
	"errors"
	"fix_bridge/codec"
	"fix_bridge/session"
	"net"
	"os"
)

func validateTestFixture(enabled bool, address string, tls bool) error {
	mode := os.Getenv("FXP_FIX_TEST_FIXTURE")
	if !enabled && mode == "" {
		return nil
	}
	host, _, err := net.SplitHostPort(address)
	if err != nil || !net.ParseIP(host).IsLoopback() || mode != "quickfix" || !enabled || tls || os.Getenv("FXP_TLS_REQUIRED") == "true" || os.Getenv("FXP_CORE_AUTH_TOKEN") != "" {
		return errors.New("QuickFIX test fixture requires -test-fixture, FXP_FIX_TEST_FIXTURE=quickfix, literal loopback, and no TLS/core authentication; forbidden in secure mode")
	}
	return nil
}

type quickfixFixture struct {
	injected *codec.Message
	seen     map[string]bool
}

func testFixtureHandler(conn net.Conn) session.Handler {
	addr, ok := conn.LocalAddr().(*net.TCPAddr)
	peer, peerOK := conn.RemoteAddr().(*net.TCPAddr)
	if !ok || !peerOK || !addr.IP.IsLoopback() || !peer.IP.IsLoopback() {
		return nil
	}
	return &quickfixFixture{seen: make(map[string]bool)}
}
func (h *quickfixFixture) PrepareInbound(m *codec.Message) {
	h.injected = nil
	if m.MsgType() == "D" {
		if _, present := m.Fields[38]; !present {
			m.Fields[38] = "1"
			// The parsed checksum trailer remains last; virtual minimum fields
			// must not create a body-after-trailer dictionary violation.
			at := len(m.Tags)
			for i, f := range m.Tags {
				if f.Tag == 10 || f.Tag == 93 || f.Tag == 89 {
					at = i
					break
				}
			}
			m.Tags = append(m.Tags, codec.Field{})
			copy(m.Tags[at+1:], m.Tags[at:])
			m.Tags[at] = codec.Field{Tag: 38, Value: "1"}
			h.injected = m
		}
	}
}
func (h *quickfixFixture) OnLogon(*session.Session)          {}
func (h *quickfixFixture) OnLogout(*session.Session, string) {}
func (h *quickfixFixture) Handle(m *codec.Message, s *session.Session) {
	if m.MsgType() != "D" {
		s.SendWithSeq(func(seq int) []byte { return buildUnsupportedReject(m, s.SenderCompID, s.TargetCompID, seq) })
		return
	}
	id := m.Get(11)
	if m.Get(97) == "Y" && h.seen[id] {
		return
	}
	if len(h.seen) >= 4096 && !h.seen[id] {
		s.Close()
		return
	}
	h.seen[id] = true
	b := codec.NewBuilder(m.MsgType(), s.SenderCompID, s.TargetCompID, 0)
	for _, f := range m.Tags {
		switch f.Tag {
		case 8, 9, 10, 35, 49, 56, 34, 52, 43, 122:
			continue
		}
		if f.Tag == 38 && m == h.injected {
			continue
		}
		b.Append(f.Tag, f.Value)
	}
	s.SendMsg(b)
}

func (h *quickfixFixture) TestRequestID() string { return "TEST" }
