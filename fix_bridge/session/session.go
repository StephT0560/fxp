// SPDX-License-Identifier: MIT

// session.go — FIX 4.4 session state machine
//
// Manages the lifecycle of a single FIX session:
//   - Logon authentication (CompID allow-list + Password(554)) and Logout
//   - Inbound sequence validation for every message, PossDup duplicates,
//     gap detection (ResendRequest) and SequenceReset (GapFill and Reset)
//   - Outbound sequencing with a MessageStore that serves ResendRequests
//   - Heartbeats, TestRequest/Heartbeat(TestReqID) and peer timeout
//   - Garbled messages are ignored; well-formed invalid ones get Reject(3),
//     including application messages missing a codec.RequiredFields tag
//
// Goroutines: readLoop (inbound, owns inSeqNum/resendTo), writeLoop (socket
// writes) and heartbeatLoop. Shared state is guarded by stateMu, sendMu,
// hbMu or atomics. Application messages go to the Handler.

package session

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"fix_bridge/codec"
)

// State represents the FIX session lifecycle state.
type State int

const (
	StateConnected    State = iota // TCP connected, waiting for Logon
	StateLoggedOn                  // Logon exchanged, processing application messages
	StateLoggingOut                // Logout sent/received, draining
	StateDisconnected              // TCP closed
)

func (s State) String() string {
	switch s {
	case StateConnected:
		return "CONNECTED"
	case StateLoggedOn:
		return "LOGGED_ON"
	case StateLoggingOut:
		return "LOGGING_OUT"
	case StateDisconnected:
		return "DISCONNECTED"
	default:
		return "UNKNOWN"
	}
}

// Handler is called by the session for each inbound application message.
// The session has already validated the sequence number before calling Handle.
// Implementations must not block — use a goroutine or channel if needed.
type Handler interface {
	Handle(msg *codec.Message, session *Session)
	OnLogon(session *Session)
	OnLogout(session *Session, reason string)
}

// DisconnectHandler is optionally implemented by a Handler to run once the
// session's connection has closed for any reason, before its store is released.
type DisconnectHandler interface {
	OnDisconnect(session *Session)
}

// Session manages one FIX session over a TCP connection.
type Session struct {
	conn    net.Conn
	parser  *codec.Parser
	handler Handler

	// Auth validates Logon credentials; a nil Auth rejects every Logon.
	Auth                Authenticator
	StrictDictionary    bool
	SilentLogonFailures bool
	WaitPeerLogout      bool
	QuickFIXCompatible  bool
	// Store keeps sent messages for ResendRequest and the sequence numbers.
	// With a Registry it is replaced at Logon by the session's durable store.
	Store MessageStore
	// Registry, if set, supplies the store keyed by (BeginString,
	// SenderCompID, TargetCompID) so sequence state survives reconnects.
	Registry                                       *Registry
	LogonTimeout                                   time.Duration
	LogoutTimeout                                  time.Duration
	MaxLatency                                     time.Duration
	Schedule                                       *Schedule
	Now                                            func() time.Time
	ResetOnLogon, ResetOnLogout, ResetOnDisconnect bool
	resetAtClose                                   atomic.Bool
	scheduleEpoch                                  atomic.Int64
	loggedOn                                       atomic.Bool

	// Identity
	SenderCompID string // bridge's CompID
	TargetCompID string // client's CompID; written under stateMu at Logon
	// CancelOnDisconnect is Logon tag 8013=Y; written under stateMu at Logon.
	CancelOnDisconnect bool

	outSeqNum int64                  // next outbound MsgSeqNum (guarded by sendMu)
	inSeqNum  int                    // next expected inbound MsgSeqNum (readLoop only)
	pending   map[int]*codec.Message // readLoop only; bounded out-of-order admin messages
	replyTo   *codec.Message         // readLoop-only context for immediate session replies
	resendTo  int                    // highest seq covered by our pending ResendRequest, 0 = none (readLoop only)

	regKey   *SessionKey // registry entry held since Logon (guarded by sendMu)
	released bool        // registry entry released; Store must not be written (guarded by sendMu)

	heartbeatUnit time.Duration // fixed before Run; tests may shorten it
	heartBtInt    int           // guarded by stateMu
	lastSent      time.Time     // guarded by sendMu
	lastReceived  atomic.Int64  // unix nanos

	hbMu          sync.Mutex // guards the outstanding TestRequest
	testReqID     string
	testReqSentAt time.Time

	state   State
	stateMu sync.RWMutex

	sendMu  sync.Mutex // serialises seq allocation + enqueue
	sendCh  chan []byte
	queued  atomic.Int64
	written atomic.Int64

	done          chan struct{}
	once          sync.Once
	heartbeatOnce sync.Once
}

// NewSession creates a session for an accepted TCP connection.
func NewSession(conn net.Conn, senderCompID string, handler Handler) *Session {
	s := &Session{
		conn:          conn,
		Now:           time.Now,
		parser:        codec.NewParser(conn),
		handler:       handler,
		Store:         NewMemoryStore(),
		SenderCompID:  senderCompID,
		outSeqNum:     1,
		inSeqNum:      1,
		heartbeatUnit: time.Second,
		heartBtInt:    30, // default; overridden by client's Logon HeartBtInt
		lastSent:      time.Now(),
		state:         StateConnected,
		sendCh:        make(chan []byte, 512),
		done:          make(chan struct{}),
		LogonTimeout:  10 * time.Second,
		LogoutTimeout: 5 * time.Second,
		MaxLatency:    120 * time.Second,
	}
	s.parser.SetMaxBodyLength(4096)
	s.lastReceived.Store(time.Now().UnixNano())
	return s
}

// sendEnqueueTimeout bounds how long a send waits for space in sendCh.
var sendEnqueueTimeout = 5 * time.Second

// flushTimeout bounds how long a closing session waits to write queued frames.
const flushTimeout = time.Second

// Run starts the session read loop and writer, blocking until the session ends.
func (s *Session) Run() {
	defer func() {
		s.flush(flushTimeout)
		// A handler with disconnect work (the production venue/COD path)
		// retains its store until its final reports have been persisted.
		if dh, ok := s.handler.(DisconnectHandler); ok {
			s.close()
			dh.OnDisconnect(s)
			s.releaseStore()
		} else {
			// Echo/session-only handlers have no late persistence work. Release
			// before TCP EOF so immediate reconnect cannot race this cleanup.
			s.releaseStore()
			s.close()
		}
	}()
	go s.writeLoop()
	if s.Schedule != nil {
		go s.scheduleLoop()
	}
	if s.LogonTimeout > 0 {
		go s.logonTimeoutLoop()
	}
	s.readLoop()
}

func (s *Session) logonTimeoutLoop() {
	timer := time.NewTimer(s.LogonTimeout)
	defer timer.Stop()
	select {
	case <-s.done:
		return
	case <-timer.C:
		if s.State() == StateConnected {
			log.Printf("[%s] Logon timeout after %v — closing", s.target(), s.LogonTimeout)
			s.close()
		}
	}
}

// SendWithSeq allocates the next outbound MsgSeqNum, builds the frame with it,
// records it in the Store and enqueues it under one lock, so frames reach the
// wire in sequence order with no gaps. A nil frame consumes no number; a frame
// the Store cannot persist is never sent and the session closes.
func (s *Session) SendWithSeq(build func(seq int) []byte) {
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	if s.released {
		return
	}
	seq := int(s.outSeqNum)
	frame := build(seq)
	if frame == nil {
		return
	}
	if err := s.Store.Save(seq, frame); err != nil {
		log.Printf("[%s] %v — closing session", s.target(), err)
		s.close()
		return
	}
	s.outSeqNum++
	s.enqueueLocked(frame)
}

// enqueueLocked queues a frame; the caller holds sendMu. A client that stops
// reading is disconnected rather than having a numbered frame dropped.
func (s *Session) enqueueLocked(frame []byte) bool {
	timer := time.NewTimer(sendEnqueueTimeout)
	defer timer.Stop()
	select {
	case s.sendCh <- frame:
		s.queued.Add(1)
		s.lastSent = time.Now()
		return true
	case <-s.done:
		return false
	case <-timer.C:
		log.Printf("[%s] send queue full for %v — closing session", s.target(), sendEnqueueTimeout)
		s.close()
		return false
	}
}

// SendMsg builds and enqueues a FIX message using the session's sequence number.
func (s *Session) SendMsg(b *codec.Builder) {
	s.SendWithSeq(func(seq int) []byte {
		b.SetInt(codec.TagMsgSeqNum, seq)
		b.Set(codec.TagSenderCompID, s.SenderCompID)
		b.Set(codec.TagTargetCompID, s.target())
		return b.Build()
	})
}

func (s *Session) target() string {
	s.stateMu.RLock()
	defer s.stateMu.RUnlock()
	return s.TargetCompID
}

func (s *Session) sinceLastSent() time.Duration {
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	return time.Since(s.lastSent)
}

func (s *Session) sinceLastReceived() time.Duration {
	return time.Since(time.Unix(0, s.lastReceived.Load()))
}

func (s *Session) heartbeatInterval() time.Duration {
	s.stateMu.RLock()
	defer s.stateMu.RUnlock()
	return time.Duration(s.heartBtInt) * s.heartbeatUnit
}

// State returns the current session state.
func (s *Session) State() State {
	s.stateMu.RLock()
	defer s.stateMu.RUnlock()
	return s.state
}

func (s *Session) setState(st State) {
	s.stateMu.Lock()
	s.state = st
	s.stateMu.Unlock()
}

// ── Read loop ─────────────────────────────────────────────────────────────────

func (s *Session) readLoop() {
	for {
		msg, err := s.parser.ReadMessage()
		if errors.Is(err, codec.ErrBeginString) {
			reason := fmt.Sprintf("unsupported BeginString %q", msg.Get(codec.TagBeginString))
			if st := s.State(); st != StateLoggedOn && st != StateLoggingOut {
				s.rejectLogon(msg, reason)
			} else {
				log.Printf("[%s] %s", s.target(), reason)
				s.finishFatal(reason, msg)
			}
			return
		}
		if err != nil {
			if errors.Is(err, codec.ErrGarbled) {
				slog.Warn("ignoring garbled message", "session", s.target(), "err", err)
				continue
			}
			if err != io.EOF {
				log.Printf("[%s] read error: %v", s.target(), err)
			}
			return
		}
		s.lastReceived.Store(time.Now().UnixNano())

		if err := s.dispatch(msg); err != nil {
			if err == io.EOF {
				return // Logout exchanged or Logon rejected; reply already queued
			}
			log.Printf("[%s] dispatch error: %v", s.target(), err)
			s.finishFatal(err.Error(), msg)
			return
		}
	}
}

// dispatch validates the sequence number of every inbound message, then
// routes it to the session-level handlers or the application Handler.
func (s *Session) dispatch(msg *codec.Message) error {
	err := s.dispatchOne(msg)
	for err == nil {
		next := s.pending[s.inSeqNum]
		if next == nil {
			break
		}
		delete(s.pending, s.inSeqNum)
		err = s.dispatchOne(next)
	}
	return err
}

func (s *Session) dispatchOne(msg *codec.Message) error {
	s.replyTo = msg
	defer func() { s.replyTo = nil }()

	msgType := msg.MsgType()
	state := s.State()
	seq, err := strconv.Atoi(msg.Get(codec.TagMsgSeqNum))
	ignoreResetSeq := (state == StateLoggedOn || state == StateLoggingOut) && msgType == codec.MsgTypeSequenceReset && msg.Get(codec.TagGapFillFlag) != "Y" && err == nil && seq == 0
	if err != nil || seq <= 0 && !ignoreResetSeq {
		if state != StateLoggedOn && state != StateLoggingOut {
			return s.rejectLogon(msg, "missing or invalid MsgSeqNum (34)")
		}
		return fmt.Errorf("missing or invalid MsgSeqNum (34)")
	}

	if state != StateLoggedOn && state != StateLoggingOut {
		if msgType != codec.MsgTypeLogon {
			return s.rejectLogon(msg, fmt.Sprintf("first message must be Logon, got 35=%s", msgType))
		}
		return s.handleLogon(msg, seq)
	}

	if v := codec.DefaultDictionary.ValidateIdentity(msg); v != nil {
		if seq == s.inSeqNum {
			if err := s.setInSeq(seq + 1); err != nil {
				return err
			}
		}
		s.sendRejectRef(seq, v.Tag, msgType, v.Reason, v.Text)
		return nil
	}

	if msg.Get(codec.TagSenderCompID) != s.target() || msg.Get(codec.TagTargetCompID) != s.SenderCompID {
		s.sendRejectRef(seq, 0, msgType, codec.RejectCompIDProblem, "CompID problem")
		return fmt.Errorf("CompID problem: 49=%q 56=%q", msg.Get(codec.TagSenderCompID), msg.Get(codec.TagTargetCompID))
	}

	// SequenceReset-Reset and Logon(141=Y) ignore MsgSeqNum.
	if msgType == codec.MsgTypeSequenceReset && msg.Get(codec.TagGapFillFlag) != "Y" {
		if v := codec.DefaultDictionary.Validate(msg, s.StrictDictionary); v != nil {
			s.sendRejectRef(seq, v.Tag, msgType, v.Reason, v.Text)
			return nil
		}
		s.applyNewSeqNo(msg, seq)
		return nil
	}
	if msgType == codec.MsgTypeLogon && msg.Get(codec.TagResetSeqNumFlag) == "Y" {
		s.handleInSessionReset(seq)
		return nil
	}

	possDup := msg.Get(codec.TagPossDupFlag) == "Y"
	switch {
	case seq < s.inSeqNum:
		// ResendRequest is safe to serve again: replay never allocates numbers
		// or executes application messages. Validate before bypassing low-seq.
		if msgType == codec.MsgTypeResendRequest {
			if v := codec.DefaultDictionary.Validate(msg, s.StrictDictionary); v != nil {
				s.sendRejectRef(seq, v.Tag, msgType, v.Reason, v.Text)
				return nil
			}
			if reason, text := s.validateSendingTime(msg); reason != "" {
				s.sendReject(seq, reason, text)
				return nil
			}
			if possDup {
				if reason, text := checkPossDup(msg); reason != "" {
					s.sendPossDupReject(msg, seq, reason, text)
					if reason == codec.RejectSendingTimeAccuracy {
						s.finishFatal(text, msg)
						return io.EOF
					}
					return nil
				}
			}
			s.handleResendRequest(msg)
			return nil
		}
		// A peer's terminal Logout is not application execution and may reuse
		// the final sequence after an idempotent ResendRequest.
		if msgType == codec.MsgTypeLogout {
			return s.handleLogout(msg)
		}
		if !possDup {
			return fmt.Errorf("MsgSeqNum too low, expecting %d but received %d", s.inSeqNum, seq)
		}
		if reason, text := checkPossDup(msg); reason != "" {
			s.sendPossDupReject(msg, seq, reason, text)
			if reason == codec.RejectSendingTimeAccuracy {
				s.finishFatal(text, msg)
				return io.EOF
			}
		}
		return nil // duplicate of a message already processed
	case seq > s.inSeqNum:
		switch msgType {
		case codec.MsgTypeResendRequest:
			s.handleResendRequest(msg)
		case codec.MsgTypeLogout:
			return s.handleLogout(msg)
		}
		if msgType != codec.MsgTypeResendRequest {
			if s.pending == nil {
				s.pending = make(map[int]*codec.Message)
			}
			size := 0
			for _, queued := range s.pending {
				for _, f := range queued.Tags {
					size += len(f.Value) + 16
				}
			}
			for _, f := range msg.Tags {
				size += len(f.Value) + 16
			}
			if len(s.pending) >= 64 || size > 256*1024 || seq-s.inSeqNum > 4096 {
				return fmt.Errorf("replay buffer limit exceeded")
			}
			if s.pending[seq] == nil {
				s.pending[seq] = msg
			}
		}
		s.requestResend(seq)
		return nil
	}
	if err := s.setInSeq(s.inSeqNum + 1); err != nil {
		return err
	}
	if s.resendTo != 0 && s.inSeqNum > s.resendTo {
		s.resendTo = 0
	}

	// Well-formed but invalid: consume the sequence number and Reject(3).
	if possDup {
		if reason, text := checkPossDup(msg); reason != "" {
			s.sendPossDupReject(msg, seq, reason, text)
			if reason == codec.RejectSendingTimeAccuracy {
				s.finishFatal(text, msg)
				return io.EOF
			}
			return nil
		}
	}
	if msg.Get(codec.TagSendingTime) == "" {
		s.sendReject(seq, codec.RejectRequiredTagMissing, "SendingTime (52) missing")
		return nil
	}
	if reason, text := s.validateSendingTime(msg); reason != "" {
		s.sendRejectRef(seq, 0, msgType, reason, text)
		if reason == codec.RejectSendingTimeAccuracy {
			s.finishFatal(text, msg)
			return io.EOF
		}
		return nil
	}
	if !validMsgType(msgType) {
		s.sendRejectRef(seq, 0, msgType, codec.RejectInvalidMsgType, fmt.Sprintf("invalid MsgType %q", msgType))
		return nil
	}
	if support, ok := s.handler.(interface{ SupportsApplicationMessage(string) bool }); ok && !isAdminMsgType(msgType) && !support.SupportsApplicationMessage(msgType) {
		s.handler.Handle(msg, s)
		return nil
	}
	if prepare, ok := s.handler.(interface{ PrepareInbound(*codec.Message) }); ok {
		prepare.PrepareInbound(msg)
	}
	if v := codec.DefaultDictionary.Validate(msg, s.StrictDictionary); v != nil {
		s.sendRejectRef(seq, v.Tag, msgType, v.Reason, v.Text)
		return nil
	}

	switch msgType {
	case codec.MsgTypeLogon:
		return nil // repeated Logon without reset: nothing to do
	case codec.MsgTypeLogout:
		return s.handleLogout(msg)
	case codec.MsgTypeHeartbeat:
		s.handleHeartbeat(msg)
	case codec.MsgTypeTestRequest:
		s.handleTestRequest(msg)
	case codec.MsgTypeResendRequest:
		s.handleResendRequest(msg)
	case codec.MsgTypeReject:
		slog.Warn("Reject received", "session", s.target(), "ref_seq", msg.Get(codec.TagRefSeqNum),
			"reason", msg.Get(codec.TagSessionRejectReason), "text", msg.Get(codec.TagText))
	case codec.MsgTypeSequenceReset:
		s.applyNewSeqNo(msg, seq) // GapFill
	default:
		s.handler.Handle(msg, s)
	}
	return nil
}

func validMsgType(t string) bool {
	if len(t) == 0 || len(t) > 2 {
		return false
	}
	for _, c := range t {
		if !(c >= '0' && c <= '9' || c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z') {
			return false
		}
	}
	return true
}

func isAdminMsgType(t string) bool {
	switch t {
	case codec.MsgTypeLogon, codec.MsgTypeHeartbeat, codec.MsgTypeTestRequest, codec.MsgTypeResendRequest,
		codec.MsgTypeReject, codec.MsgTypeSequenceReset, codec.MsgTypeLogout:
		return true
	}
	return false
}

const fixTimeLayout = "20060102-15:04:05.000"

func parseFIXTime(v string) (time.Time, error) {
	if t, err := time.Parse(fixTimeLayout, v); err == nil {
		return t, nil
	}
	return time.Parse("20060102-15:04:05", v)
}

func (s *Session) validateSendingTime(msg *codec.Message) (string, string) {
	if s.MaxLatency <= 0 {
		return "", ""
	}
	sendingTime, err := parseFIXTime(msg.Get(codec.TagSendingTime))
	if err != nil {
		return codec.RejectIncorrectDataFormat, "SendingTime (52) is not a UTCTimestamp"
	}
	now := time.Now()
	latency := now.Sub(sendingTime)
	if sendingTime.After(now) {
		latency = sendingTime.Sub(now)
	}
	if latency > s.MaxLatency {
		return codec.RejectSendingTimeAccuracy, "SendingTime (52) outside MaxLatency"
	}
	return "", ""
}

// checkPossDup validates OrigSendingTime(122) on a PossDupFlag=Y message.
func checkPossDup(msg *codec.Message) (reason, text string) {
	orig := msg.Get(codec.TagOrigSendingTime)
	if orig == "" {
		return codec.RejectRequiredTagMissing, "OrigSendingTime (122) required with PossDupFlag=Y"
	}
	ot, err1 := parseFIXTime(orig)
	st, err2 := parseFIXTime(msg.Get(codec.TagSendingTime))
	if err1 != nil || err2 != nil {
		return codec.RejectIncorrectDataFormat, "OrigSendingTime/SendingTime not a UTCTimestamp"
	}
	if ot.After(st) {
		return codec.RejectSendingTimeAccuracy, "OrigSendingTime later than SendingTime"
	}
	return "", ""
}

// ── Logon / Logout ────────────────────────────────────────────────────────────

func (s *Session) handleLogon(msg *codec.Message, seq int) error {
	sender := msg.Get(codec.TagSenderCompID)
	if sender == "" {
		return s.rejectLogon(msg, "Logon missing SenderCompID (49)")
	}
	if msg.Get(codec.TagTargetCompID) != s.SenderCompID {
		return s.rejectLogon(msg, "invalid TargetCompID (56)")
	}
	if s.Auth == nil || s.Auth.Authenticate(sender, msg.Get(codec.TagPassword)) != nil {
		return s.rejectLogon(msg, "authentication failed")
	}
	if reason, text := s.validateSendingTime(msg); reason != "" {
		return s.rejectLogon(msg, text)
	}

	if s.Schedule != nil && !s.Schedule.Contains(s.Now()) {
		return s.rejectLogon(msg, "Logon outside configured session schedule")
	}
	cod := msg.Get(codec.TagCancelOnDisconnect)
	if cod != "" && cod != "Y" && cod != "N" {
		return s.rejectLogon(msg, "invalid CancelOnDisconnect (8013); expected Y or N")
	}
	if v := codec.DefaultDictionary.Validate(msg, s.StrictDictionary); v != nil {
		if s.SilentLogonFailures {
			return s.rejectLogon(msg, v.Text)
		}
		s.SendWithSeq(func(n int) []byte {
			return codec.ReverseRoute(codec.NewRejectRef(s.SenderCompID, sender, n, seq, v.Tag, msg.MsgType(), v.Reason, v.Text), msg)
		})
		return s.rejectLogon(msg, v.Text)
	}
	reset := msg.Get(codec.TagResetSeqNumFlag) == "Y" || s.ResetOnLogon
	if reset && seq != 1 {
		return s.rejectLogon(msg, "ResetSeqNumFlag=Y requires MsgSeqNum=1")
	}
	if s.Registry != nil {
		if err := s.acquireStore(SessionKey{msg.Get(codec.TagBeginString), s.SenderCompID, sender}); err != nil {
			return s.rejectLogon(msg, err.Error())
		}
	}
	s.sendMu.Lock()
	scheduleErr := s.advanceSchedule()
	s.sendMu.Unlock()
	if scheduleErr != nil {
		return scheduleErr
	}
	if reset {
		s.resetOutbound()
		if err := s.setInSeq(1); err != nil {
			return err
		}
	}
	if seq < s.inSeqNum {
		return s.rejectLogon(msg, fmt.Sprintf("MsgSeqNum too low, expecting %d but received %d", s.inSeqNum, seq))
	}

	gap := seq > s.inSeqNum
	if !gap {
		if err := s.setInSeq(seq + 1); err != nil {
			return err
		}
	}

	s.stateMu.Lock()
	s.TargetCompID = sender
	s.CancelOnDisconnect = cod == "Y"
	if hbi := msg.GetInt(codec.TagHeartBtInt); hbi > 0 {
		s.heartBtInt = hbi
	}
	hbi := s.heartBtInt
	s.state = StateLoggedOn
	s.parser.SetMaxBodyLength(1_048_576)
	s.stateMu.Unlock()
	log.Printf("[%s] Logon accepted (HeartBtInt=%ds, reset=%v)", sender, hbi, reset)

	s.SendWithSeq(func(n int) []byte {
		return codec.ReverseRoute(codec.NewLogonAck(s.SenderCompID, sender, n, hbi, reset), msg)
	})
	if gap {
		s.requestResend(seq)
	}
	s.heartbeatOnce.Do(func() { go s.heartbeatLoop() })
	s.loggedOn.Store(true)
	s.handler.OnLogon(s)
	return nil
}

// rejectLogon answers an unacceptable Logon with a Logout and ends the session.
func (s *Session) rejectLogon(msg *codec.Message, reason string) error {
	sender := msg.Get(codec.TagSenderCompID)
	log.Printf("[%s] Logon rejected: %s", sender, reason)
	if s.SilentLogonFailures {
		return io.EOF
	}
	s.SendWithSeq(func(n int) []byte {
		return codec.ReverseRoute(codec.NewLogout(s.SenderCompID, sender, n, reason), msg)
	})
	return io.EOF
}

func (s *Session) handleInSessionReset(seq int) {
	if seq != 1 {
		s.sendReject(seq, codec.RejectValueIncorrect, "Logon with ResetSeqNumFlag=Y must have MsgSeqNum=1")
		return
	}
	log.Printf("[%s] Logon with ResetSeqNumFlag=Y — resetting sequence numbers", s.target())
	s.pending = nil
	s.resetOutbound()
	if s.setInSeq(2) != nil {
		return
	}
	s.resendTo = 0
	hbi := int(s.heartbeatInterval() / s.heartbeatUnit)
	s.SendWithSeq(func(n int) []byte {
		return codec.NewLogonAck(s.SenderCompID, s.target(), n, hbi, true)
	})
}

// acquireStore takes the session's registry entry and resumes its sequence numbers.
func (s *Session) acquireStore(key SessionKey) error {
	store, err := s.Registry.Acquire(key)
	if err != nil {
		return err
	}
	in, out := store.SeqNums()
	s.sendMu.Lock()
	s.Store, s.regKey = store, &key
	s.outSeqNum = int64(out)
	s.sendMu.Unlock()
	s.inSeqNum = in
	log.Printf("[%s] resuming session state: next in=%d out=%d", key.TargetCompID, in, out)
	return nil
}

// releaseStore hands the registry entry back once no more frames can be
// stored (close may run with sendMu held, so this runs after it, from Run).
func (s *Session) releaseStore() {
	s.sendMu.Lock()
	s.released = true
	// Reset and freeze the old session under the same lock. Gateway callbacks
	// already in flight must not rewrite the fresh store before registry release.
	if s.loggedOn.Load() && (s.resetAtClose.Load() || s.ResetOnDisconnect) {
		reset := true
		if s.Schedule != nil && s.resetAtClose.Load() {
			if err := s.advanceSchedule(); err != nil {
				log.Printf("scheduled archive failed; preserving old store: %v", err)
				reset = false
			}
		}
		if reset {
			s.Store.Reset()
			s.inSeqNum = 1
			s.outSeqNum = 1
			s.resendTo = 0
		}
	}
	key := s.regKey
	s.sendMu.Unlock()
	if key != nil {
		s.Registry.Release(*key)
	}
}

// setInSeq advances the next expected inbound MsgSeqNum and persists it. If
// it cannot be persisted the session closes rather than run ahead of its store.
func (s *Session) setInSeq(n int) error {
	s.inSeqNum = n
	for seq := range s.pending {
		if seq < n {
			delete(s.pending, seq)
		}
	}
	if err := s.Store.SetNextInSeq(n); err != nil {
		log.Printf("[%s] %v — closing session", s.target(), err)
		s.close()
		return err
	}
	return nil
}

func (s *Session) resetOutbound() {
	s.sendMu.Lock()
	s.outSeqNum = 1
	s.Store.Reset()
	s.sendMu.Unlock()
}

func (s *Session) handleLogout(msg *codec.Message) error {
	if s.ResetOnLogout {
		s.resetAtClose.Store(true)
	}
	reason := msg.Get(codec.TagText)
	log.Printf("[%s] Logout received: %q", s.target(), reason)
	if s.State() != StateLoggingOut {
		s.setState(StateLoggingOut)
		s.SendWithSeq(func(seq int) []byte {
			return codec.ReverseRoute(codec.NewLogout(s.SenderCompID, s.target(), seq, ""), msg)
		})
	}
	if s.handler != nil {
		s.handler.OnLogout(s, reason)
	}
	return io.EOF
}

// Logout initiates a graceful Logout exchange with the peer.
func (s *Session) Logout(reason string) {
	if s.ResetOnLogout {
		s.resetAtClose.Store(true)
	}
	s.stateMu.Lock()
	if s.state != StateLoggedOn {
		s.stateMu.Unlock()
		return
	}
	s.state = StateLoggingOut
	timeout := s.LogoutTimeout
	s.stateMu.Unlock()

	s.SendWithSeq(func(seq int) []byte {
		return codec.NewLogout(s.SenderCompID, s.target(), seq, reason)
	})
	if timeout <= 0 {
		return
	}
	go func() {
		timer := time.NewTimer(timeout)
		defer timer.Stop()
		select {
		case <-s.done:
		case <-timer.C:
			log.Printf("[%s] Logout timeout after %v — closing", s.target(), timeout)
			s.close()
		}
	}()
}

func (s *Session) sendLogout(reason string, request ...*codec.Message) {
	var original *codec.Message
	if len(request) > 0 {
		original = request[0]
	}
	if s.State() == StateLoggedOn {
		s.setState(StateLoggingOut)
		s.SendWithSeq(func(seq int) []byte {
			return codec.ReverseRoute(codec.NewLogout(s.SenderCompID, s.target(), seq, reason), original)
		})
	}
}

func (s *Session) sendReject(refSeq int, reason, text string) {
	s.sendRejectRef(refSeq, 0, "", reason, text)
}

func (s *Session) sendRejectRef(refSeq, refTagID int, refMsgType, reason, text string) {
	original := s.replyTo
	slog.Warn("rejecting message", "session", s.target(), "ref_seq", refSeq, "reason", reason, "text", text)
	s.SendWithSeq(func(n int) []byte {
		return codec.ReverseRoute(codec.NewRejectRef(s.SenderCompID, s.target(), n, refSeq, refTagID, refMsgType, reason, text), original)
	})
}

// ── Heartbeat / TestRequest ───────────────────────────────────────────────────

func (s *Session) handleHeartbeat(msg *codec.Message) {
	id := msg.Get(codec.TagTestReqID)
	if id == "" {
		return
	}
	s.hbMu.Lock()
	defer s.hbMu.Unlock()
	if id == s.testReqID {
		s.testReqID = ""
	} else {
		log.Printf("[%s] Heartbeat TestReqID %q does not match outstanding %q", s.target(), id, s.testReqID)
	}
}

func (s *Session) handleTestRequest(msg *codec.Message) {
	testReqID := msg.Get(codec.TagTestReqID)
	s.SendWithSeq(func(seq int) []byte {
		return codec.ReverseRoute(codec.NewHeartbeat(s.SenderCompID, s.target(), seq, testReqID), msg)
	})
}

// ── Sequence recovery ─────────────────────────────────────────────────────────

// requestResend asks for everything from the next expected seq onwards, once
// per gap; seq is the out-of-order message that revealed it.
func (s *Session) requestResend(seq int) {
	if s.resendTo != 0 {
		if seq > s.resendTo {
			s.resendTo = seq
		}
		return
	}
	s.resendTo = seq
	begin := s.inSeqNum
	log.Printf("[%s] sequence gap: expected %d, got %d — sending ResendRequest", s.target(), begin, seq)
	original := s.replyTo
	s.SendWithSeq(func(n int) []byte {
		b := codec.NewBuilder(codec.MsgTypeResendRequest, s.SenderCompID, s.target(), n)
		b.SetInt(codec.TagBeginSeqNo, begin)
		b.SetInt(codec.TagEndSeqNo, 0)
		return codec.ReverseRoute(b.Build(), original)
	})
}

// applyNewSeqNo handles SequenceReset: GapFill (after the seq check) or Reset.
func (s *Session) applyNewSeqNo(msg *codec.Message, seq int) {
	newSeq := msg.GetInt(codec.TagNewSeqNo)
	if newSeq <= 0 {
		s.sendReject(seq, codec.RejectRequiredTagMissing, "NewSeqNo (36) missing")
		return
	}
	if newSeq < s.inSeqNum {
		s.sendRejectRef(seq, 0, codec.MsgTypeSequenceReset, codec.RejectValueIncorrect,
			fmt.Sprintf("NewSeqNo %d lower than expected %d", newSeq, s.inSeqNum))
		return
	}
	if s.setInSeq(newSeq) != nil {
		return
	}
	if s.resendTo != 0 && s.inSeqNum > s.resendTo {
		s.resendTo = 0
	}
}

// handleResendRequest replays stored application messages with PossDupFlag=Y
// and replaces administrative or missing ones with SequenceReset-GapFill.
func (s *Session) handleResendRequest(msg *codec.Message) {
	begin := msg.GetInt(codec.TagBeginSeqNo)
	end := msg.GetInt(codec.TagEndSeqNo)

	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	last := int(s.outSeqNum) - 1
	if begin < 1 {
		begin = 1
	}
	if end == 0 || end > last {
		end = last
	}
	target := s.target()
	log.Printf("[%s] ResendRequest %d..%d", target, begin, end)

	gapStart := 0
	flushGap := func(next int) bool {
		if gapStart == 0 {
			return true
		}
		b := codec.NewBuilder(codec.MsgTypeSequenceReset, s.SenderCompID, target, gapStart)
		b.Set(codec.TagPossDupFlag, "Y")
		b.Set(codec.TagOrigSendingTime, time.Now().UTC().Format(fixTimeLayout))
		b.Set(codec.TagGapFillFlag, "Y")
		b.SetInt(codec.TagNewSeqNo, next)
		gapStart = 0
		return s.enqueueLocked(b.Build())
	}
	for seq := begin; seq <= end; seq++ {
		m := s.storedMessage(seq)
		if m == nil || isAdminMsgType(m.MsgType()) {
			if gapStart == 0 {
				gapStart = seq
			}
			continue
		}
		if !flushGap(seq) || !s.enqueueLocked(resendFrame(m, s.SenderCompID, target, seq)) {
			return
		}
	}
	flushGap(end + 1)
}

func (s *Session) storedMessage(seq int) *codec.Message {
	raw, ok := s.Store.Get(seq)
	if !ok {
		return nil
	}
	m, err := codec.NewParser(bytes.NewReader(raw)).ReadMessage()
	if err != nil {
		return nil
	}
	return m
}

// resendFrame rebuilds a stored message as a retransmission: same MsgSeqNum,
// PossDupFlag=Y, OrigSendingTime = original SendingTime, fresh SendingTime.
func resendFrame(m *codec.Message, sender, target string, seq int) []byte {
	b := codec.NewBuilder(m.MsgType(), sender, target, seq)
	b.Set(codec.TagPossDupFlag, "Y")
	b.Set(codec.TagOrigSendingTime, m.Get(codec.TagSendingTime))
	for _, f := range m.Tags {
		switch f.Tag {
		case codec.TagBeginString, codec.TagBodyLength, codec.TagCheckSum, codec.TagMsgType,
			codec.TagSenderCompID, codec.TagTargetCompID, codec.TagMsgSeqNum, codec.TagSendingTime,
			codec.TagPossDupFlag, codec.TagOrigSendingTime:
			continue
		}
		b.Append(f.Tag, f.Value)
	}
	return b.Build()
}

// ── Heartbeat loop ────────────────────────────────────────────────────────────

// heartbeatLoop sends Heartbeats when idle, sends a TestRequest when the peer
// has been silent for HeartBtInt (+20%), and disconnects if no Heartbeat with
// the matching TestReqID arrives within another HeartBtInt.
func (s *Session) heartbeatLoop() {
	for {
		interval := s.heartbeatInterval()
		select {
		case <-s.done:
			return
		case <-time.After(interval / 4):
		}
		if s.State() != StateLoggedOn {
			continue
		}

		s.hbMu.Lock()
		pending, sentAt := s.testReqID, s.testReqSentAt
		s.hbMu.Unlock()
		if pending != "" {
			if time.Since(sentAt) >= interval {
				log.Printf("[%s] no Heartbeat for TestRequest %s within %v — disconnecting", s.target(), pending, interval)
				if !s.QuickFIXCompatible {
					s.sendLogout("TestRequest timeout")
				}
				s.flush(flushTimeout)
				s.close()
				return
			}
			continue
		}
		if s.sinceLastSent() >= interval {
			s.SendWithSeq(func(seq int) []byte {
				return codec.NewHeartbeat(s.SenderCompID, s.target(), seq, "")
			})
		}

		if s.sinceLastReceived() >= interval+interval/5 {
			id := fmt.Sprintf("TEST-%d", time.Now().UnixNano())
			if fixture, ok := s.handler.(interface{ TestRequestID() string }); ok {
				id = fixture.TestRequestID()
			}
			s.hbMu.Lock()
			s.testReqID, s.testReqSentAt = id, time.Now()
			s.hbMu.Unlock()
			s.SendWithSeq(func(seq int) []byte {
				b := codec.NewBuilder(codec.MsgTypeTestRequest, s.SenderCompID, s.target(), seq)
				b.Set(codec.TagTestReqID, id)
				return b.Build()
			})
		}
	}
}

// ── Write loop ────────────────────────────────────────────────────────────────

func (s *Session) writeLoop() {
	for {
		select {
		case <-s.done:
			return
		case frame := <-s.sendCh:
			if _, err := s.conn.Write(frame); err != nil {
				log.Printf("[%s] write error: %v", s.target(), err)
				s.close()
				return
			}
			s.written.Add(1)
		}
	}
}

// flush waits (bounded) until every queued frame has been written.
func (s *Session) flush(timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for s.written.Load() < s.queued.Load() && time.Now().Before(deadline) {
		select {
		case <-s.done:
			return
		case <-time.After(2 * time.Millisecond):
		}
	}
}

// ── Cleanup ───────────────────────────────────────────────────────────────────

func (s *Session) close() {
	s.once.Do(func() {
		s.setState(StateDisconnected)
		close(s.done)
		s.conn.Close()
		log.Printf("[%s] session closed", s.target())
	})
}

func (s *Session) Close() {
	s.close()
}
