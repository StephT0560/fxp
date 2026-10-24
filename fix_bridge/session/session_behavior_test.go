package session

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"fix_bridge/codec"
)

// testPassword is generated per run so no credential literal lives in the repo.
var testPassword = func() string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}()

type recHandler struct {
	mu      sync.Mutex
	app     []*codec.Message
	logons  int
	logouts int
}

func (h *recHandler) Handle(m *codec.Message, _ *Session) {
	h.mu.Lock()
	h.app = append(h.app, m)
	h.mu.Unlock()
}
func (h *recHandler) OnLogon(*Session) { h.mu.Lock(); h.logons++; h.mu.Unlock() }
func (h *recHandler) OnLogout(*Session, string) {
	h.mu.Lock()
	h.logouts++
	h.mu.Unlock()
}
func (h *recHandler) appCount() int    { h.mu.Lock(); defer h.mu.Unlock(); return len(h.app) }
func (h *recHandler) logoutCount() int { h.mu.Lock(); defer h.mu.Unlock(); return h.logouts }

// peer is the client end of a session under test.
type peer struct {
	t              *testing.T
	conn           net.Conn
	in             chan *codec.Message
	closed         chan struct{}
	sess           *Session
	h              *recHandler
	sender, target string
	seq            int
}

func newPeer(t *testing.T) *peer {
	return newPeerConfigured(t, nil)
}

func newPeerConfigured(t *testing.T, configure func(*Session)) *peer {
	t.Helper()
	srv, cli := net.Pipe()
	h := &recHandler{}
	s := NewSession(srv, "FXPBRIDGE", h)
	s.Auth = NewCredentialAuth(map[string]string{"CLIENT": testPassword})
	if configure != nil {
		configure(s)
	}
	p := &peer{t: t, conn: cli, in: make(chan *codec.Message, 1024), closed: make(chan struct{}),
		sess: s, h: h, sender: "CLIENT", target: "FXPBRIDGE"}
	go s.Run()
	go func() {
		defer close(p.closed)
		pr := codec.NewParser(cli)
		for {
			m, err := pr.ReadMessage()
			if err != nil {
				return
			}
			p.in <- m
		}
	}()
	t.Cleanup(func() { cli.Close(); s.close() })
	return p
}

func fastHeartbeats(t *testing.T) {
	old := heartbeatUnit
	heartbeatUnit = 20 * time.Millisecond
	t.Cleanup(func() { heartbeatUnit = old })
}

func (p *peer) frame(msgType string, seq int, kv ...string) []byte {
	b := codec.NewBuilder(msgType, p.sender, p.target, seq)
	for i := 0; i+1 < len(kv); i += 2 {
		tag, _ := strconv.Atoi(kv[i])
		b.Set(tag, kv[i+1])
	}
	return b.Build()
}

func (p *peer) write(b []byte) {
	p.t.Helper()
	_ = p.conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
	if _, err := p.conn.Write(b); err != nil {
		p.t.Fatalf("write: %v", err)
	}
}

func (p *peer) sendSeq(msgType string, seq int, kv ...string) {
	p.t.Helper()
	p.write(p.frame(msgType, seq, kv...))
}
func (p *peer) send(msgType string, kv ...string) {
	p.t.Helper()
	p.seq++
	p.sendSeq(msgType, p.seq, kv...)
}

func (p *peer) logon(kv ...string) *codec.Message {
	p.t.Helper()
	p.seq = 1
	p.sendSeq(codec.MsgTypeLogon, 1, append([]string{"98", "0", "108", "30", "554", testPassword}, kv...)...)
	return p.expect(codec.MsgTypeLogon)
}

// expect returns the next non-Heartbeat message, which must be msgType.
func (p *peer) expect(msgType string) *codec.Message {
	p.t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case m := <-p.in:
			if m.MsgType() == codec.MsgTypeHeartbeat && msgType != codec.MsgTypeHeartbeat {
				continue
			}
			if m.MsgType() != msgType {
				p.t.Fatalf("got 35=%s (58=%q), want 35=%s", m.MsgType(), m.Get(codec.TagText), msgType)
			}
			return m
		case <-deadline:
			p.t.Fatalf("timed out waiting for 35=%s", msgType)
		}
	}
}

// expectQuiet asserts no non-Heartbeat message arrives within d.
func (p *peer) expectQuiet(d time.Duration) {
	p.t.Helper()
	deadline := time.After(d)
	for {
		select {
		case m := <-p.in:
			if m.MsgType() != codec.MsgTypeHeartbeat {
				p.t.Fatalf("unexpected 35=%s (58=%q)", m.MsgType(), m.Get(codec.TagText))
			}
		case <-deadline:
			return
		}
	}
}

func (p *peer) expectClosed() {
	p.t.Helper()
	select {
	case <-p.closed:
	case <-time.After(2 * time.Second):
		p.t.Fatal("session not disconnected")
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func fixTime(t time.Time) string { return t.UTC().Format("20060102-15:04:05.000") }

// rawFrame builds a frame from a literal body ("35=D\x01...") so header
// fields can be omitted.
func rawFrame(body string) []byte {
	prefix := fmt.Sprintf("8=FIX.4.4\x019=%d\x01", len(body))
	sum := 0
	for _, c := range []byte(prefix + body) {
		sum += int(c)
	}
	return []byte(fmt.Sprintf("%s%s10=%03d\x01", prefix, body, sum%256))
}

func TestParseCredentials(t *testing.T) {
	creds, err := ParseCredentials("A:x, B:y")
	if err != nil || len(creds) != 2 || creds["A"] != "x" || creds["B"] != "y" {
		t.Fatalf("ParseCredentials = %v, %v", creds, err)
	}
	for _, bad := range []string{"", "A", "A:", ":x"} {
		if _, err := ParseCredentials(bad); err == nil {
			t.Errorf("ParseCredentials(%q) accepted", bad)
		}
	}
}

func TestLogonAuthentication(t *testing.T) {
	cases := []struct {
		name, sender, target, password string
	}{
		{"unknown CompID", "EVIL", "FXPBRIDGE", testPassword},
		{"bad password", "CLIENT", "FXPBRIDGE", "wrong"},
		{"missing password", "CLIENT", "FXPBRIDGE", ""},
		{"wrong TargetCompID", "CLIENT", "OTHER", testPassword},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := newPeer(t)
			p.sender, p.target = tc.sender, tc.target
			p.sendSeq(codec.MsgTypeLogon, 1, "98", "0", "108", "30", "554", tc.password)
			lo := p.expect(codec.MsgTypeLogout)
			if lo.Get(codec.TagText) == "" {
				t.Error("Logout carries no reason")
			}
			p.expectClosed()
			if p.h.logons != 0 {
				t.Error("handler saw a rejected Logon")
			}
		})
	}
	t.Run("valid", func(t *testing.T) {
		p := newPeer(t)
		ack := p.logon()
		if ack.GetInt(codec.TagMsgSeqNum) != 1 {
			t.Errorf("LogonAck 34=%s", ack.Get(codec.TagMsgSeqNum))
		}
		waitFor(t, "OnLogon", func() bool { p.h.mu.Lock(); defer p.h.mu.Unlock(); return p.h.logons == 1 })
	})
}

func TestLogonRejectsOtherBeginString(t *testing.T) {
	p := newPeer(t)
	p.write(withBeginString(p.frame(codec.MsgTypeLogon, 1,
		"98", "0", "108", "30"), "FIX.4.2"))

	logout := p.expect(codec.MsgTypeLogout)
	if got := logout.Get(codec.TagText); got != `unsupported BeginString "FIX.4.2"` {
		t.Fatalf("rejected Logon reason = %q", got)
	}
	p.h.mu.Lock()
	logons := p.h.logons
	p.h.mu.Unlock()
	if logons != 0 {
		t.Fatalf("OnLogon called %d times for mismatched BeginString", logons)
	}
}

func TestLogonTimeoutClosesWithoutLogout(t *testing.T) {
	p := newPeerConfigured(t, func(s *Session) { s.LogonTimeout = 30 * time.Millisecond })
	p.expectClosed()
	p.expectQuiet(20 * time.Millisecond)
}

func TestInitiatedLogoutHandshake(t *testing.T) {
	p := newPeerConfigured(t, func(s *Session) { s.LogoutTimeout = 250 * time.Millisecond })
	p.logon()
	p.sess.Logout("gateway unavailable")
	if logout := p.expect(codec.MsgTypeLogout); logout.Get(codec.TagText) != "gateway unavailable" {
		t.Fatalf("Logout text = %q", logout.Get(codec.TagText))
	}
	p.send(codec.MsgTypeLogout, strconv.Itoa(codec.TagText), "ack")
	p.expectClosed()
	p.expectQuiet(20 * time.Millisecond)
	if got := p.h.logoutCount(); got != 1 {
		t.Fatalf("OnLogout called %d times, want 1", got)
	}
}

func TestInitiatedLogoutTimeoutCloses(t *testing.T) {
	p := newPeerConfigured(t, func(s *Session) { s.LogoutTimeout = 30 * time.Millisecond })
	p.logon()
	p.sess.Logout("shutdown")
	p.expect(codec.MsgTypeLogout)
	p.expectClosed()
}

func TestInvalidLogonSendingTimeRejected(t *testing.T) {
	cases := []struct {
		name string
		time string
	}{
		{"stale", fixTime(time.Now().Add(-2 * time.Minute))},
		{"malformed", "not-a-time"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := newPeerConfigured(t, func(s *Session) { s.MaxLatency = 30 * time.Millisecond })
			p.sendSeq(codec.MsgTypeLogon, 1, "98", "0", "108", "30", "554", testPassword,
				strconv.Itoa(codec.TagSendingTime), tc.time)
			if logout := p.expect(codec.MsgTypeLogout); logout.Get(codec.TagText) == "" {
				t.Fatal("rejected Logon Logout has no reason")
			}
			p.expectClosed()
		})
	}
}

func TestInvalidApplicationSendingTime(t *testing.T) {
	t.Run("stale disconnects", func(t *testing.T) {
		p := newPeerConfigured(t, func(s *Session) { s.MaxLatency = 30 * time.Millisecond })
		p.logon()
		p.sendSeq("D", 2, strconv.Itoa(codec.TagClOrdID), "O1",
			strconv.Itoa(codec.TagSendingTime), fixTime(time.Now().Add(-2*time.Minute)))
		reject := p.expect(codec.MsgTypeReject)
		if reject.Get(codec.TagSessionRejectReason) != codec.RejectSendingTimeAccuracy {
			t.Fatalf("Reject 373=%q, want %s", reject.Get(codec.TagSessionRejectReason), codec.RejectSendingTimeAccuracy)
		}
		p.expect(codec.MsgTypeLogout)
		p.expectClosed()
		if got := p.h.appCount(); got != 0 {
			t.Fatalf("stale application message reached handler; count=%d", got)
		}
	})

	t.Run("malformed gets format reject", func(t *testing.T) {
		p := newPeerConfigured(t, func(s *Session) { s.MaxLatency = time.Second })
		p.logon()
		p.sendSeq("D", 2, strconv.Itoa(codec.TagClOrdID), "O1", strconv.Itoa(codec.TagSendingTime), "not-a-time")
		reject := p.expect(codec.MsgTypeReject)
		if reject.Get(codec.TagSessionRejectReason) != codec.RejectIncorrectDataFormat {
			t.Fatalf("Reject 373=%q, want %s", reject.Get(codec.TagSessionRejectReason), codec.RejectIncorrectDataFormat)
		}
		if p.sess.State() != StateLoggedOn {
			t.Fatalf("malformed SendingTime disconnected session: %s", p.sess.State())
		}
	})
}

func TestInToleranceApplicationSendingTimeAccepted(t *testing.T) {
	p := newPeerConfigured(t, func(s *Session) { s.MaxLatency = time.Second })
	p.logon()
	p.sendSeq("D", 2, strconv.Itoa(codec.TagClOrdID), "O1",
		strconv.Itoa(codec.TagSendingTime), fixTime(time.Now().Add(-100*time.Millisecond)))
	waitFor(t, "in-tolerance application message", func() bool { return p.h.appCount() == 1 })
}

func TestBodyLengthLimitBeforeAndAfterLogon(t *testing.T) {
	p := newPeer(t)
	payload := strings.Repeat("x", 5000)
	p.write(p.frame(codec.MsgTypeLogout, 1, strconv.Itoa(codec.TagText), payload))

	p.logon()
	p.sendSeq(codec.MsgTypeTestRequest, 2, strconv.Itoa(codec.TagTestReqID), payload)
	heartbeat := p.expect(codec.MsgTypeHeartbeat)
	if got := heartbeat.Get(codec.TagTestReqID); len(got) != len(payload) {
		t.Fatalf("TestReqID length = %d, want %d", len(got), len(payload))
	}
}

func TestApplicationMessageRejectsOtherBeginString(t *testing.T) {
	p := newPeer(t)
	p.logon()
	p.write(withBeginString(p.frame(codec.MsgTypeHeartbeat, 2), "FIX.4.2"))

	logout := p.expect(codec.MsgTypeLogout)
	if logout.Get(codec.TagText) == "" {
		t.Fatal("mismatched BeginString Logout has no reason")
	}
	select {
	case <-p.closed:
	case <-time.After(time.Second):
		t.Fatal("session remained open after BeginString mismatch")
	}
}

func TestLogonResetSeqNumFlag(t *testing.T) {
	p := newPeer(t)
	if ack := p.logon("141", "Y"); ack.Get(codec.TagResetSeqNumFlag) != "Y" || ack.GetInt(codec.TagMsgSeqNum) != 1 {
		t.Fatalf("LogonAck to 141=Y: 141=%q 34=%s", ack.Get(codec.TagResetSeqNumFlag), ack.Get(codec.TagMsgSeqNum))
	}
	p2 := newPeer(t)
	if ack := p2.logon(); ack.Get(codec.TagResetSeqNumFlag) != "" {
		t.Fatalf("LogonAck without 141=Y in Logon carries 141=%q", ack.Get(codec.TagResetSeqNumFlag))
	}
}

func withBeginString(frame []byte, beginString string) []byte {
	wire := strings.Replace(string(frame), "FIX.4.4", beginString, 1)
	checksumStart := strings.LastIndex(wire, "10=")
	sum := 0
	for _, b := range []byte(wire[:checksumStart]) {
		sum += int(b)
	}
	return []byte(wire[:checksumStart] + fmt.Sprintf("10=%03d\x01", sum%256))
}

func TestInboundSeqAdvancesForAdminMessages(t *testing.T) {
	p := newPeer(t)
	p.logon()
	p.send(codec.MsgTypeHeartbeat)
	p.send(codec.MsgTypeTestRequest, "112", "T1")
	if hb := p.expect(codec.MsgTypeHeartbeat); hb.Get(codec.TagTestReqID) != "T1" {
		t.Fatalf("Heartbeat 112=%q, want T1", hb.Get(codec.TagTestReqID))
	}
	// Reusing seq 3 (consumed by the TestRequest) without PossDup is fatal.
	p.sendSeq(codec.MsgTypeHeartbeat, 3)
	if lo := p.expect(codec.MsgTypeLogout); lo.Get(codec.TagText) == "" {
		t.Error("Logout carries no reason")
	}
	p.expectClosed()
}

func TestPossDupDuplicateIsDropped(t *testing.T) {
	p := newPeer(t)
	p.logon()
	p.send("D", "11", "O1")
	waitFor(t, "first order", func() bool { return p.h.appCount() == 1 })
	p.sendSeq("D", 2, "11", "O1", "43", "Y", "122", fixTime(time.Now().Add(-time.Second)))
	p.send("D", "11", "O2")
	waitFor(t, "second order", func() bool { return p.h.appCount() == 2 })
	p.expectQuiet(50 * time.Millisecond)
	// PossDup without OrigSendingTime is rejected, not processed.
	p.sendSeq("D", 2, "11", "O1", "43", "Y")
	if r := p.expect(codec.MsgTypeReject); r.Get(codec.TagSessionRejectReason) != codec.RejectRequiredTagMissing {
		t.Errorf("Reject 373=%q", r.Get(codec.TagSessionRejectReason))
	}
	if p.h.appCount() != 2 {
		t.Fatalf("duplicates reached the handler: %d app messages", p.h.appCount())
	}
}

func TestGapTriggersResendRequest(t *testing.T) {
	p := newPeer(t)
	p.logon()
	p.sendSeq("D", 5, "11", "O5")
	rr := p.expect(codec.MsgTypeResendRequest)
	if rr.Get(codec.TagBeginSeqNo) != "2" || rr.Get(codec.TagEndSeqNo) != "0" {
		t.Fatalf("ResendRequest 7=%s 16=%s, want 7=2 16=0", rr.Get(codec.TagBeginSeqNo), rr.Get(codec.TagEndSeqNo))
	}
	if p.h.appCount() != 0 {
		t.Fatal("out-of-sequence message was processed")
	}
	for seq := 2; seq <= 5; seq++ {
		p.sendSeq("D", seq, "11", fmt.Sprintf("O%d", seq), "43", "Y", "122", fixTime(time.Now()))
	}
	waitFor(t, "replayed orders", func() bool { return p.h.appCount() == 4 })
	p.expectQuiet(50 * time.Millisecond)
}

func TestResendRequestReplaysStoredMessages(t *testing.T) {
	p := newPeer(t)
	p.logon()
	for _, id := range []string{"A", "B"} {
		p.sess.SendMsg(codec.NewBuilder(codec.MsgTypeExecutionReport, "", "", 0).Set(codec.TagClOrdID, id))
	}
	first := p.expect(codec.MsgTypeExecutionReport)
	p.expect(codec.MsgTypeExecutionReport)

	p.send(codec.MsgTypeResendRequest, "7", "1", "16", "0")
	gf := p.expect(codec.MsgTypeSequenceReset)
	if gf.Get(codec.TagMsgSeqNum) != "1" || gf.Get(codec.TagGapFillFlag) != "Y" || gf.Get(codec.TagNewSeqNo) != "2" || gf.Get(codec.TagPossDupFlag) != "Y" {
		t.Fatalf("GapFill for LogonAck: 34=%s 123=%s 36=%s 43=%s", gf.Get(34), gf.Get(123), gf.Get(36), gf.Get(43))
	}
	for i, id := range []string{"A", "B"} {
		m := p.expect(codec.MsgTypeExecutionReport)
		if m.GetInt(codec.TagMsgSeqNum) != i+2 || m.Get(codec.TagClOrdID) != id || m.Get(codec.TagPossDupFlag) != "Y" || m.Get(codec.TagOrigSendingTime) == "" {
			t.Fatalf("resent ER %d: 34=%s 11=%s 43=%s 122=%s", i, m.Get(34), m.Get(11), m.Get(43), m.Get(122))
		}
	}
	if m := first; m.Get(codec.TagSendingTime) == "" {
		t.Fatal("original ER has no SendingTime")
	}
	// New messages continue the original sequence.
	p.sess.SendMsg(codec.NewBuilder(codec.MsgTypeExecutionReport, "", "", 0).Set(codec.TagClOrdID, "C"))
	if m := p.expect(codec.MsgTypeExecutionReport); m.GetInt(codec.TagMsgSeqNum) != 4 || m.Get(codec.TagPossDupFlag) != "" {
		t.Fatalf("post-resend ER 34=%s 43=%s", m.Get(34), m.Get(43))
	}
}

func TestSequenceResetGapFillAndReset(t *testing.T) {
	p := newPeer(t)
	p.logon()
	p.send(codec.MsgTypeSequenceReset, "123", "Y", "36", "6")
	p.seq = 5
	p.send("D", "11", "O6")
	waitFor(t, "order after GapFill", func() bool { return p.h.appCount() == 1 })

	// Reset mode ignores MsgSeqNum.
	p.sendSeq(codec.MsgTypeSequenceReset, 1, "36", "20")
	p.seq = 19
	p.send("D", "11", "O20")
	waitFor(t, "order after Reset", func() bool { return p.h.appCount() == 2 })

	// Resetting backwards is rejected.
	p.sendSeq(codec.MsgTypeSequenceReset, 1, "36", "5")
	if r := p.expect(codec.MsgTypeReject); r.Get(codec.TagSessionRejectReason) != codec.RejectValueIncorrect {
		t.Errorf("Reject 373=%q", r.Get(codec.TagSessionRejectReason))
	}
	p.send("D", "11", "O21")
	waitFor(t, "order after rejected Reset", func() bool { return p.h.appCount() == 3 })
	p.expectQuiet(50 * time.Millisecond)
}

func TestInboundRejectAdvancesSeq(t *testing.T) {
	p := newPeer(t)
	p.logon()
	p.send(codec.MsgTypeReject, "45", "1", "58", "bad")
	p.send("D", "11", "O1")
	waitFor(t, "order", func() bool { return p.h.appCount() >= 1 })
	p.expectQuiet(50 * time.Millisecond)
	if n := p.h.appCount(); n != 1 {
		t.Fatalf("handler received %d messages, want only the order", n)
	}
}

func TestGarbledMessagesAreIgnored(t *testing.T) {
	p := newPeer(t)
	p.logon()
	p.write([]byte("garbage\x01"))
	bad := p.frame("D", 2, "11", "BAD")
	n := len(bad)
	cs, _ := strconv.Atoi(string(bad[n-4 : n-1]))
	copy(bad[n-4:], fmt.Sprintf("%03d", (cs+1)%256))
	p.write(bad)
	p.send("D", "11", "GOOD")
	waitFor(t, "valid order after garbage", func() bool { return p.h.appCount() == 1 })
	if id := p.h.app[0].Get(codec.TagClOrdID); id != "GOOD" {
		t.Fatalf("handler got %q", id)
	}
	p.expectQuiet(50 * time.Millisecond)
}

func TestInvalidMessageGetsSessionReject(t *testing.T) {
	p := newPeer(t)
	p.logon()
	p.send("ZZZ")
	r := p.expect(codec.MsgTypeReject)
	if r.Get(codec.TagRefSeqNum) != "2" || r.Get(codec.TagSessionRejectReason) != codec.RejectInvalidMsgType {
		t.Fatalf("Reject 45=%s 373=%s", r.Get(codec.TagRefSeqNum), r.Get(codec.TagSessionRejectReason))
	}
	p.write(rawFrame("35=D\x0149=CLIENT\x0156=FXPBRIDGE\x0134=3\x0111=NOTIME\x01"))
	r = p.expect(codec.MsgTypeReject)
	if r.Get(codec.TagRefSeqNum) != "3" || r.Get(codec.TagSessionRejectReason) != codec.RejectRequiredTagMissing {
		t.Fatalf("Reject 45=%s 373=%s", r.Get(codec.TagRefSeqNum), r.Get(codec.TagSessionRejectReason))
	}
	p.seq = 3
	p.send("D", "11", "O4")
	waitFor(t, "valid order", func() bool { return p.h.appCount() == 1 })
	p.expectQuiet(50 * time.Millisecond)
}

func TestTestRequestTimeoutDisconnects(t *testing.T) {
	fastHeartbeats(t)
	p := newPeer(t)
	p.seq = 1
	p.sendSeq(codec.MsgTypeLogon, 1, "98", "0", "108", "1", "554", testPassword)
	p.expect(codec.MsgTypeLogon)
	tr := p.expect(codec.MsgTypeTestRequest)
	// A Heartbeat with the wrong TestReqID does not answer it.
	p.send(codec.MsgTypeHeartbeat, "112", tr.Get(codec.TagTestReqID)+"-wrong")
	p.expect(codec.MsgTypeLogout)
	p.expectClosed()
}

// Exercises readLoop, writeLoop and heartbeatLoop concurrently (run with -race).
func TestSessionGoroutinesRaceFree(t *testing.T) {
	fastHeartbeats(t)
	p := newPeer(t)
	p.seq = 1
	p.sendSeq(codec.MsgTypeLogon, 1, "98", "0", "108", "1", "554", testPassword)
	p.expect(codec.MsgTypeLogon)

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			case <-time.After(3 * time.Millisecond):
				p.sess.SendMsg(codec.NewBuilder(codec.MsgTypeExecutionReport, "", "", 0).Set(codec.TagClOrdID, strconv.Itoa(i)))
			}
		}
	}()

	deadline := time.After(400 * time.Millisecond)
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
loop:
	for {
		select {
		case m := <-p.in:
			switch m.MsgType() {
			case codec.MsgTypeTestRequest:
				p.send(codec.MsgTypeHeartbeat, "112", m.Get(codec.TagTestReqID))
			case codec.MsgTypeLogout:
				t.Fatalf("unexpected Logout: %q", m.Get(codec.TagText))
			}
		case <-tick.C:
			p.send(codec.MsgTypeHeartbeat)
		case <-deadline:
			break loop
		}
	}
	close(stop)
	wg.Wait()
	if p.sess.State() != StateLoggedOn {
		t.Fatalf("session state %v", p.sess.State())
	}
}

func TestResetLogonRequiresSeqNumOne(t *testing.T) {
	p := newPeer(t)
	p.seq = 1
	p.sendSeq(codec.MsgTypeLogon, 5, "98", "0", "108", "30", "554", testPassword, "141", "Y")
	p.expect(codec.MsgTypeLogout)
	p.expectClosed()

	p = newPeer(t)
	p.logon()
	p.send("D", "11", "O1")
	p.sendSeq(codec.MsgTypeLogon, 25, "98", "0", "108", "30", "141", "Y")
	if r := p.expect(codec.MsgTypeReject); r.Get(codec.TagSessionRejectReason) != codec.RejectValueIncorrect {
		t.Fatalf("Reject 373=%q", r.Get(codec.TagSessionRejectReason))
	}
	p.sendSeq(codec.MsgTypeLogon, 1, "98", "0", "108", "30", "141", "Y")
	if ack := p.expect(codec.MsgTypeLogon); ack.Get(codec.TagResetSeqNumFlag) != "Y" || ack.GetInt(codec.TagMsgSeqNum) != 1 {
		t.Fatalf("reset ack 141=%q 34=%s", ack.Get(codec.TagResetSeqNumFlag), ack.Get(codec.TagMsgSeqNum))
	}
	p.sendSeq("D", 2, "11", "O2")
	waitFor(t, "order after reset", func() bool { return p.h.appCount() == 2 })
	p.expectQuiet(50 * time.Millisecond)
}

func TestForgedSequenceResetRejected(t *testing.T) {
	p := newPeer(t)
	p.logon()
	p.sender = "EVIL"
	p.sendSeq(codec.MsgTypeSequenceReset, 2, "36", "100")
	if r := p.expect(codec.MsgTypeReject); r.Get(codec.TagSessionRejectReason) != codec.RejectCompIDProblem {
		t.Fatalf("Reject 373=%q", r.Get(codec.TagSessionRejectReason))
	}
	p.expect(codec.MsgTypeLogout)
	p.expectClosed()
}

func TestPossDupMalformedOrigSendingTimeRejected(t *testing.T) {
	p := newPeer(t)
	p.logon()
	p.send("D", "11", "O1", "43", "Y", "122", "not-a-date")
	if r := p.expect(codec.MsgTypeReject); r.Get(codec.TagSessionRejectReason) != codec.RejectIncorrectDataFormat {
		t.Fatalf("Reject 373=%q", r.Get(codec.TagSessionRejectReason))
	}
	if p.h.appCount() != 0 {
		t.Fatal("malformed PossDup reached the handler")
	}
}
