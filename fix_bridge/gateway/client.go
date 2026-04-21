// client.go — FXP gateway client
//
// Manages a single TCP connection from the fix_bridge to the FXP Go gateway.
// Each FIX session gets its own gateway client — one FIX client = one gateway
// connection. This keeps routing simple: ExecutionReports flow back on the
// same connection that orders were sent on.
//
// Responsibilities:
//   - Connect to the gateway (:8082) and authenticate with a JWT
//   - Send FXP Protobuf frames (NewOrderSingle, Cancel, CancelReplace)
//   - Receive FXP Protobuf frames (ExecutionReport, MarketData)
//   - Route received frames to a callback for translation back to FIX
//
// The JWT is generated from the FIX client's SenderCompID, threading the
// client identity through to Rust's ExecutionReport without any gateway changes.

package gateway

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"sync"
	"time"

	"go_gateway/protobuf"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	gatewayAddr    = "127.0.0.1:8082"
	maxFrameSize   = 1_048_576 // 1MB — same limit as Rust Core
	sendBufferSize = 512
)

// ReceivedMsg is delivered to the callback for every inbound FXP frame.
type ReceivedMsg struct {
	Msg *protobuf.FXPMessage
}

// OnReceive is called for every inbound FXP message from the gateway.
// Must not block — use a goroutine or channel if processing is slow.
type OnReceive func(msg *protobuf.FXPMessage)

// Client is a single FXP gateway connection representing one FIX session.
type Client struct {
	conn       net.Conn
	traderID   string // FIX SenderCompID — used as JWT subject
	sessionID  string
	jwtSecret  string

	sendCh     chan []byte
	onReceive  OnReceive

	done       chan struct{}
	once       sync.Once
	connected  bool
}

// NewClient creates a gateway client for a FIX session.
// traderID is the FIX client's SenderCompID.
// jwtSecret is the shared secret for JWT generation (from FXP_JWT_SECRET env var).
// onReceive is called for every inbound FXP message.
func NewClient(traderID, jwtSecret string, onReceive OnReceive) *Client {
	return &Client{
		traderID:  traderID,
		sessionID: fmt.Sprintf("fix-%s-%d", traderID, time.Now().UnixNano()),
		jwtSecret: jwtSecret,
		sendCh:    make(chan []byte, sendBufferSize),
		onReceive: onReceive,
		done:      make(chan struct{}),
	}
}

// Connect dials the gateway, sends the Logon frame, and starts the read/write loops.
// Blocks until the connection is closed.
func (c *Client) Connect() error {
	conn, err := net.DialTimeout("tcp", gatewayAddr, 5*time.Second)
	if err != nil {
		return fmt.Errorf("dial gateway %s: %w", gatewayAddr, err)
	}
	c.conn = conn
	c.connected = true
	log.Printf("[gateway:%s] connected to %s", c.traderID, gatewayAddr)

	// Authenticate with Logon
	if err := c.sendLogon(); err != nil {
		conn.Close()
		return fmt.Errorf("logon failed: %w", err)
	}

	// Read LogonAck
	if err := c.readLogonAck(); err != nil {
		conn.Close()
		return fmt.Errorf("logon ack failed: %w", err)
	}

	log.Printf("[gateway:%s] authenticated as %s", c.traderID, c.traderID)

	// Start writer goroutine
	go c.writeLoop()

	// Read loop blocks until disconnect
	c.readLoop()
	return nil
}

// Send enqueues an FXP proto message for delivery to the gateway.
// Safe to call from any goroutine. Non-blocking.
func (c *Client) Send(msg *protobuf.FXPMessage) error {
	raw, err := proto.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal FXP message: %w", err)
	}
	frame := makeFrame(raw)
	select {
	case c.sendCh <- frame:
		return nil
	default:
		return fmt.Errorf("gateway send buffer full — dropping message")
	}
}

// Close gracefully shuts down the gateway connection.
func (c *Client) Close() {
	c.once.Do(func() {
		close(c.done)
		if c.conn != nil {
			c.conn.Close()
		}
	})
}

// ── Logon ─────────────────────────────────────────────────────────────────────

func (c *Client) sendLogon() error {
	token := generateJWT(c.jwtSecret, c.traderID)
	msg := &protobuf.FXPMessage{
		Payload: &protobuf.FXPMessage_Logon{
			Logon: &protobuf.Logon{
				Token:     token,
				SessionId: c.sessionID,
				Timestamp: timestamppb.Now(),
			},
		},
	}
	raw, err := proto.Marshal(msg)
	if err != nil {
		return err
	}
	_, err = c.conn.Write(makeFrame(raw))
	return err
}

func (c *Client) readLogonAck() error {
	c.conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	defer c.conn.SetReadDeadline(time.Time{})

	msg, err := readFramed(c.conn)
	if err != nil {
		return fmt.Errorf("reading logon ack: %w", err)
	}
	if _, ok := msg.Payload.(*protobuf.FXPMessage_LogonAck); !ok {
		return fmt.Errorf("expected LogonAck, got %T", msg.Payload)
	}
	ack := msg.Payload.(*protobuf.FXPMessage_LogonAck).LogonAck
	if !ack.Accepted {
		return fmt.Errorf("gateway rejected logon: %s", ack.RejectReason)
	}
	return nil
}

// ── Read / write loops ────────────────────────────────────────────────────────

func (c *Client) readLoop() {
	defer c.Close()
	for {
		msg, err := readFramed(c.conn)
		if err != nil {
			if err != io.EOF {
				select {
				case <-c.done:
					// Expected shutdown
				default:
					log.Printf("[gateway:%s] read error: %v", c.traderID, err)
				}
			}
			return
		}
		if c.onReceive != nil {
			c.onReceive(msg)
		}
	}
}

func (c *Client) writeLoop() {
	for {
		select {
		case <-c.done:
			return
		case frame := <-c.sendCh:
			if _, err := c.conn.Write(frame); err != nil {
				log.Printf("[gateway:%s] write error: %v", c.traderID, err)
				c.Close()
				return
			}
		}
	}
}

// ── Frame helpers ─────────────────────────────────────────────────────────────

// makeFrame prepends a 4-byte big-endian length prefix to a serialised proto message.
func makeFrame(raw []byte) []byte {
	frame := make([]byte, 4+len(raw))
	binary.BigEndian.PutUint32(frame[:4], uint32(len(raw)))
	copy(frame[4:], raw)
	return frame
}

// readFramed reads one length-prefixed FXP frame from conn and unmarshals it.
func readFramed(conn net.Conn) (*protobuf.FXPMessage, error) {
	lenBuf := make([]byte, 4)
	if _, err := io.ReadFull(conn, lenBuf); err != nil {
		return nil, err
	}
	msgLen := binary.BigEndian.Uint32(lenBuf)
	if msgLen == 0 || msgLen > maxFrameSize {
		return nil, fmt.Errorf("invalid frame length: %d", msgLen)
	}
	body := make([]byte, msgLen)
	if _, err := io.ReadFull(conn, body); err != nil {
		return nil, err
	}
	msg := &protobuf.FXPMessage{}
	if err := proto.Unmarshal(body, msg); err != nil {
		return nil, fmt.Errorf("unmarshal FXP message: %w", err)
	}
	return msg, nil
}

// ── JWT generation ────────────────────────────────────────────────────────────

func mustJSON(v interface{}) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

func generateJWT(secret, traderID string) string {
	header := base64.RawURLEncoding.EncodeToString(
		mustJSON(map[string]string{"alg": "HS256", "typ": "JWT"}),
	)
	payload := base64.RawURLEncoding.EncodeToString(
		mustJSON(map[string]interface{}{
			"sub": traderID,
			"iat": time.Now().Unix(),
			"exp": time.Now().Add(24 * time.Hour).Unix(),
		}),
	)
	unsigned := header + "." + payload
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(unsigned))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return unsigned + "." + sig
}