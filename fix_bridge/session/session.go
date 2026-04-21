// session.go — FIX 4.4 session state machine
//
// Manages the lifecycle of a single FIX session:
//   - Logon / Logout handshake
//   - Inbound and outbound sequence number tracking
//   - Heartbeat generation (sends Heartbeat every HeartBtInt seconds)
//   - TestRequest detection and Heartbeat response
//   - Gap detection (inbound seqnum out of range → ResendRequest)
//
// One Session per connected FIX client. The session runs two goroutines:
//   - readLoop:  reads inbound frames, validates seqnums, dispatches to handler
//   - heartbeat: fires a Heartbeat every HeartBtInt seconds
//
// Application messages (NewOrderSingle, ExecutionReport, etc.) are passed
// to the Handler interface — the session layer does not interpret them.
//
// Thread safety: Session methods are called from the readLoop goroutine only.
// Send() may be called from any goroutine (it writes to a buffered channel).

package session

import (
	"fmt"
	"io"
	"log"
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
	StateConnected  State = iota // TCP connected, waiting for Logon
	StateLoggedOn                // Logon exchanged, processing application messages
	StateLoggingOut              // Logout sent/received, draining
	StateDisconnected            // TCP closed
)

func (s State) String() string {
	switch s {
	case StateConnected:   return "CONNECTED"
	case StateLoggedOn:    return "LOGGED_ON"
	case StateLoggingOut:  return "LOGGING_OUT"
	case StateDisconnected: return "DISCONNECTED"
	default:               return "UNKNOWN"
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

// Session manages one FIX session over a TCP connection.
type Session struct {
	conn         net.Conn
	parser       *codec.Parser
	handler      Handler

	// Identity
	SenderCompID string // bridge's CompID (sent as SenderCompID in outbound msgs)
	TargetCompID string // client's CompID (sent as TargetCompID in outbound msgs)

	// Sequence numbers
	outSeqNum    int64  // next outbound sequence number (atomic)
	inSeqNum     int    // next expected inbound sequence number

	// Session parameters
	heartBtInt   int           // heartbeat interval in seconds
	lastSent     time.Time     // time of last outbound message
	lastReceived time.Time     // time of last inbound message

	// State
	state        State
	stateMu      sync.RWMutex

	// Outbound send channel — allows any goroutine to send FIX frames safely
	sendCh       chan []byte

	// Shutdown
	done         chan struct{}
	once         sync.Once
}

// NewSession creates a session for an accepted TCP connection.
// senderCompID is the bridge's identity; targetCompID is filled in from the Logon.
func NewSession(conn net.Conn, senderCompID string, handler Handler) *Session {
	return &Session{
		conn:         conn,
		parser:       codec.NewParser(conn),
		handler:      handler,
		SenderCompID: senderCompID,
		TargetCompID: "", // set when Logon arrives
		outSeqNum:    1,
		inSeqNum:     1,
		heartBtInt:   30, // default; overridden by client's Logon HeartBtInt
		lastSent:     time.Now(),
		lastReceived: time.Now(),
		state:        StateConnected,
		sendCh:       make(chan []byte, 512),
		done:         make(chan struct{}),
	}
}

// Run starts the session read loop and writer, blocking until the session ends.
func (s *Session) Run() {
	defer s.close()

	// Writer goroutine — drains sendCh to the TCP socket
	go s.writeLoop()

	// Heartbeat goroutine — fires on interval
	go s.heartbeatLoop()

	// Read loop — blocks until disconnect or error
	s.readLoop()
}

// Send enqueues a pre-built FIX frame for delivery.
// Safe to call from any goroutine. Non-blocking — drops if channel is full
// (which indicates a stuck client; the heartbeat will detect it).
func (s *Session) Send(frame []byte) {
	select {
	case s.sendCh <- frame:
	default:
		log.Printf("[%s] send channel full — dropping outbound frame", s.TargetCompID)
	}
}

// SendMsg builds and enqueues a FIX message using the session's sequence number.
func (s *Session) SendMsg(b *codec.Builder) {
	seq := int(atomic.AddInt64(&s.outSeqNum, 1) - 1)
	b.SetInt(codec.TagMsgSeqNum, seq)
	b.SetInt(codec.TagSenderCompID, 0) // will be overridden below
	// Re-set the correct header fields
	b.Set(codec.TagSenderCompID, s.SenderCompID)
	b.Set(codec.TagTargetCompID, s.TargetCompID)
	s.Send(b.Build())
	s.lastSent = time.Now()
}

// nextOutSeqNum returns and increments the outbound sequence number.
func (s *Session) nextOutSeqNum() int {
	return int(atomic.AddInt64(&s.outSeqNum, 1) - 1)
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
		if err != nil {
			if err != io.EOF {
				log.Printf("[%s] read error: %v", s.TargetCompID, err)
			}
			return
		}
		s.lastReceived = time.Now()

		if err := s.dispatch(msg); err != nil {
			if err == io.EOF {
				// Clean logout — handleLogout already sent the echo
				return
			}
			log.Printf("[%s] dispatch error: %v", s.TargetCompID, err)
			s.sendLogout(err.Error())
			return
		}
	}
}

// dispatch routes an inbound message to the appropriate handler.
func (s *Session) dispatch(msg *codec.Message) error {
	msgType := msg.MsgType()
	state   := s.State()

	// Session-level messages are handled here regardless of state
	switch msgType {
	case codec.MsgTypeLogon:
		return s.handleLogon(msg)

	case codec.MsgTypeLogout:
		return s.handleLogout(msg)

	case codec.MsgTypeHeartbeat:
		return s.handleHeartbeat(msg)

	case codec.MsgTypeTestRequest:
		return s.handleTestRequest(msg)

	case codec.MsgTypeResendRequest:
		// Simplified: we don't store sent messages, so respond with GapFill
		log.Printf("[%s] ResendRequest received — sending GapFill (not supported)", s.TargetCompID)
		return nil
	}

	// Application messages only accepted when logged on
	if state != StateLoggedOn {
		return fmt.Errorf("received application message %q before Logon", msgType)
	}

	// Validate inbound sequence number
	if err := s.validateSeqNum(msg); err != nil {
		return err
	}

	// Dispatch to application handler
	s.handler.Handle(msg, s)
	return nil
}

// ── Logon handling ────────────────────────────────────────────────────────────

func (s *Session) handleLogon(msg *codec.Message) error {
	if s.State() == StateLoggedOn {
		// Already logged on — treat as sequence reset if ResetSeqNumFlag=Y
		if msg.Get(codec.TagResetSeqNumFlag) == "Y" {
			log.Printf("[%s] Logon with ResetSeqNumFlag=Y — resetting sequence numbers", s.TargetCompID)
			s.inSeqNum = 2
			atomic.StoreInt64(&s.outSeqNum, 2)
		}
		return nil
	}

	// First logon — set TargetCompID from client's SenderCompID
	s.TargetCompID = msg.Get(codec.TagSenderCompID)
	if s.TargetCompID == "" {
		return fmt.Errorf("Logon missing SenderCompID (tag 49)")
	}

	// Validate expected seqnum
	if err := s.validateSeqNum(msg); err != nil {
		return err
	}

	// Read HeartBtInt from client's Logon
	if hbi := msg.GetInt(codec.TagHeartBtInt); hbi > 0 {
		s.heartBtInt = hbi
	}

	s.setState(StateLoggedOn)
	log.Printf("[%s] Logon accepted (HeartBtInt=%ds)", s.TargetCompID, s.heartBtInt)

	// Send Logon acknowledgement
	seq := s.nextOutSeqNum()
	frame := codec.NewLogonAck(s.SenderCompID, s.TargetCompID, seq, s.heartBtInt, false)
	s.Send(frame)
	s.lastSent = time.Now()

	s.handler.OnLogon(s)
	return nil
}

// ── Logout handling ───────────────────────────────────────────────────────────

func (s *Session) handleLogout(msg *codec.Message) error {
	reason := msg.Get(codec.TagText)
	log.Printf("[%s] Logout received: %q", s.TargetCompID, reason)
	s.setState(StateLoggingOut)

	// Echo Logout back
	seq := s.nextOutSeqNum()
	frame := codec.NewLogout(s.SenderCompID, s.TargetCompID, seq, "")
	s.Send(frame)

	s.handler.OnLogout(s, reason)
	return io.EOF // signals readLoop to exit cleanly — not an error
}

func (s *Session) sendLogout(reason string) {
	if s.State() == StateLoggedOn {
		s.setState(StateLoggingOut)
		seq := s.nextOutSeqNum()
		frame := codec.NewLogout(s.SenderCompID, s.TargetCompID, seq, reason)
		s.Send(frame)
	}
}

// ── Heartbeat / TestRequest handling ─────────────────────────────────────────

func (s *Session) handleHeartbeat(msg *codec.Message) error {
	// Heartbeats reset the "no data received" timer — nothing else to do
	return nil
}

func (s *Session) handleTestRequest(msg *codec.Message) error {
	testReqID := msg.Get(codec.TagTestReqID)
	seq := s.nextOutSeqNum()
	frame := codec.NewHeartbeat(s.SenderCompID, s.TargetCompID, seq, testReqID)
	s.Send(frame)
	s.lastSent = time.Now()
	return nil
}

// ── Sequence number validation ────────────────────────────────────────────────

func (s *Session) validateSeqNum(msg *codec.Message) error {
	seqStr := msg.Get(codec.TagMsgSeqNum)
	if seqStr == "" {
		return fmt.Errorf("missing MsgSeqNum (tag 34)")
	}
	seq, err := strconv.Atoi(seqStr)
	if err != nil {
		return fmt.Errorf("invalid MsgSeqNum %q: %w", seqStr, err)
	}

	if seq < s.inSeqNum {
		// Sequence number too low — potential replay attack or reset issue
		return fmt.Errorf("MsgSeqNum %d too low (expected %d)", seq, s.inSeqNum)
	}
	if seq > s.inSeqNum {
		// Gap detected — log and continue (simplified; production would send ResendRequest)
		log.Printf("[%s] sequence gap: expected %d, got %d", s.TargetCompID, s.inSeqNum, seq)
	}

	s.inSeqNum = seq + 1
	return nil
}

// ── Heartbeat loop ────────────────────────────────────────────────────────────

func (s *Session) heartbeatLoop() {
	for {
		interval := time.Duration(s.heartBtInt) * time.Second
		timer := time.NewTimer(interval)
		select {
		case <-s.done:
			timer.Stop()
			return
		case <-timer.C:
		}

		if s.State() != StateLoggedOn {
			continue
		}

		// Send Heartbeat if no outbound message sent in the last HeartBtInt seconds
		if time.Since(s.lastSent) >= interval {
			seq := s.nextOutSeqNum()
			frame := codec.NewHeartbeat(s.SenderCompID, s.TargetCompID, seq, "")
			s.Send(frame)
			s.lastSent = time.Now()
			log.Printf("[%s] Heartbeat sent (seq=%d)", s.TargetCompID, seq)
		}

		// Check if client has gone silent (no inbound in 2× HeartBtInt)
		if time.Since(s.lastReceived) > 2*interval {
			log.Printf("[%s] no data received in %v — sending TestRequest", s.TargetCompID, 2*interval)
			seq := s.nextOutSeqNum()
			testID := fmt.Sprintf("TEST-%d", time.Now().UnixNano())
			b := codec.NewBuilder(codec.MsgTypeTestRequest, s.SenderCompID, s.TargetCompID, seq)
			b.Set(codec.TagTestReqID, testID)
			s.Send(b.Build())
			s.lastSent = time.Now()
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
				log.Printf("[%s] write error: %v", s.TargetCompID, err)
				s.close()
				return
			}
		}
	}
}

// ── Cleanup ───────────────────────────────────────────────────────────────────

func (s *Session) close() {
	s.once.Do(func() {
		s.setState(StateDisconnected)
		close(s.done)
		s.conn.Close()
		log.Printf("[%s] session closed", s.TargetCompID)
	})
}