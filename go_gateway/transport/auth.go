package transport

// auth.go — JWT token validation and session lifecycle for the FXP gateway.
//
// Design:
//   - Every connection must send a Logon message as its FIRST message.
//   - The Logon.token field must be a valid HS256-signed JWT.
//   - Required JWT claims: sub (trader ID), exp (expiry), iat (issued at).
//   - On success: server sends LogonAck{accepted: true} and marks the session active.
//   - On failure: server sends LogonAck{accepted: false, reject_reason: ...} and closes.
//   - Any non-Logon message received before authentication is rejected with
//     ErrorMessage{error_type: SESSION_NOT_LOGGED_ON} and the connection is closed.
//
// Secret management:
//   - The HMAC secret is read from the FXP_JWT_SECRET environment variable.
//   - In production this should be injected via a secrets manager (Vault, AWS Secrets
//     Manager, etc.) — never hardcoded or committed to the repo.

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"strings"
	"time"

	"go_gateway/protobuf"

	"github.com/gorilla/websocket"
	"google.golang.org/protobuf/proto"
	timestamppb "google.golang.org/protobuf/types/known/timestamppb"
)

// ── Session registry ──────────────────────────────────────────────────────────

// sessionStore maps session_id → traderID for active authenticated sessions.
// sync.Map used for concurrent access across goroutines.
import "sync"

var sessionStore sync.Map // session_id (string) → traderID (string)

// IsAuthenticated returns true if the given session_id is active.
func IsAuthenticated(sessionID string) bool {
	_, ok := sessionStore.Load(sessionID)
	return ok
}

// GetTraderID returns the trader ID associated with the session, or "" if unknown.
func GetTraderID(sessionID string) string {
	if v, ok := sessionStore.Load(sessionID); ok {
		return v.(string)
	}
	return ""
}

// invalidateSession removes a session (called on Logout or disconnect).
func invalidateSession(sessionID string) {
	sessionStore.Delete(sessionID)
	log.Printf("🔒 Session invalidated: %s", sessionID)
}

// ── JWT validation ────────────────────────────────────────────────────────────

type jwtClaims struct {
	Sub string `json:"sub"` // trader ID
	Exp int64  `json:"exp"` // unix expiry
	Iat int64  `json:"iat"` // unix issued-at
}

// jwtSecret returns the HS256 signing secret from the environment.
// Panics at startup if unset — a missing secret is a configuration error,
// not a runtime error we should silently ignore.
func jwtSecret() []byte {
	s := os.Getenv("FXP_JWT_SECRET")
	if s == "" {
		panic("FXP_JWT_SECRET environment variable is not set")
	}
	return []byte(s)
}

// validateJWT verifies an HS256 JWT and returns the claims if valid.
// Returns an error for any of: malformed token, bad signature, expired token,
// missing required claims.
func validateJWT(token string) (*jwtClaims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, fmt.Errorf("malformed JWT: expected 3 parts, got %d", len(parts))
	}

	// Verify signature: HMAC-SHA256(base64url(header) + "." + base64url(payload))
	signingInput := parts[0] + "." + parts[1]
	mac := hmac.New(sha256.New, jwtSecret())
	mac.Write([]byte(signingInput))
	expectedSig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))

	if !hmac.Equal([]byte(parts[2]), []byte(expectedSig)) {
		return nil, fmt.Errorf("JWT signature verification failed")
	}

	// Decode payload
	payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("JWT payload decode failed: %w", err)
	}

	var claims jwtClaims
	if err := json.Unmarshal(payloadBytes, &claims); err != nil {
		return nil, fmt.Errorf("JWT claims unmarshal failed: %w", err)
	}

	// Validate required claims
	if claims.Sub == "" {
		return nil, fmt.Errorf("JWT missing required claim: sub")
	}
	if claims.Exp == 0 {
		return nil, fmt.Errorf("JWT missing required claim: exp")
	}
	if claims.Iat == 0 {
		return nil, fmt.Errorf("JWT missing required claim: iat")
	}
	if time.Now().Unix() > claims.Exp {
		return nil, fmt.Errorf("JWT expired at %d", claims.Exp)
	}

	return &claims, nil
}

// ── Logon handling ────────────────────────────────────────────────────────────

// HandleLogon validates the Logon message and sends a LogonAck.
// Returns (traderID, nil) on success, ("", error) on failure.
// The caller is responsible for closing the connection on failure.
func HandleLogon(logon *protobuf.Logon) (traderID string, err error) {
	claims, err := validateJWT(logon.Token)
	if err != nil {
		return "", fmt.Errorf("token validation failed: %w", err)
	}

	// Register the session
	sessionStore.Store(logon.SessionId, claims.Sub)
	log.Printf("✅ Logon accepted: session=%s trader=%s", logon.SessionId, claims.Sub)

	return claims.Sub, nil
}

// ── Wire helpers (shared with tcp_server / ws_server) ────────────────────────

func sendLogonAck(conn any, accepted bool, reason string) {
	ack := &protobuf.FXPMessage{
		Payload: &protobuf.FXPMessage_LogonAck{
			LogonAck: &protobuf.LogonAck{
				Header: &protobuf.FXPHeader{
					ProtocolVersion: "1.0",
					SequenceNumber:  0,
				},
				Accepted:     accepted,
				RejectReason: reason,
				Timestamp:    timestamppb.Now(),
			},
		},
	}

	raw, err := proto.Marshal(ack)
	if err != nil {
		log.Printf("❌ Failed to encode LogonAck: %v", err)
		return
	}

	frame := make([]byte, 4+len(raw))
	binary.BigEndian.PutUint32(frame[:4], uint32(len(raw)))
	copy(frame[4:], raw)

	switch c := conn.(type) {
	case net.Conn:
		_, _ = c.Write(frame)
	case *websocket.Conn:
		_ = c.WriteMessage(websocket.BinaryMessage, raw) // WS has its own framing
	}
}

func sendSessionError(conn any, errType protobuf.ErrorType, msg string) {
	errMsg := &protobuf.FXPMessage{
		Payload: &protobuf.FXPMessage_ErrorMessage{
			ErrorMessage: &protobuf.ErrorMessage{
				Header: &protobuf.FXPHeader{
					ProtocolVersion: "1.0",
					SequenceNumber:  0,
				},
				ErrorType:    errType,
				ErrorMessage: msg,
				Timestamp:    timestamppb.Now(),
			},
		},
	}

	raw, err := proto.Marshal(errMsg)
	if err != nil {
		log.Printf("❌ Failed to encode ErrorMessage: %v", err)
		return
	}

	frame := make([]byte, 4+len(raw))
	binary.BigEndian.PutUint32(frame[:4], uint32(len(raw)))
	copy(frame[4:], raw)

	switch c := conn.(type) {
	case net.Conn:
		_, _ = c.Write(frame)
	case *websocket.Conn:
		_ = c.WriteMessage(websocket.BinaryMessage, raw)
	}
}

// ── Auth-aware message router ─────────────────────────────────────────────────

// HandleFXPMessageAuthenticated wraps HandleFXPMessage with session enforcement.
// It must be called instead of HandleFXPMessage for all incoming messages.
//
// sessionID is passed in from the connection handler, which tracks whether
// Logon has been completed for this specific connection.
func HandleFXPMessageAuthenticated(conn any, msg *protobuf.FXPMessage, sessionID *string) {
	switch payload := msg.Payload.(type) {

	case *protobuf.FXPMessage_Logon:
		// Logon is always allowed — it's how you authenticate
		logon := payload.Logon

		traderID, err := HandleLogon(logon)
		if err != nil {
			log.Printf("❌ Logon rejected for session %s: %v", logon.SessionId, err)
			sendLogonAck(conn, false, err.Error())
			closeConn(conn)
			return
		}

		// Store the session ID on this connection's context
		*sessionID = logon.SessionId
		sendLogonAck(conn, true, "")
		log.Printf("🔓 Session %s authenticated as trader %s", logon.SessionId, traderID)

	case *protobuf.FXPMessage_Logout:
		// Logout is allowed whether authenticated or not
		if *sessionID != "" {
			invalidateSession(*sessionID)
			*sessionID = ""
		}
		closeConn(conn)

	default:
		// All other message types require an active session
		if *sessionID == "" || !IsAuthenticated(*sessionID) {
			log.Printf("⛔ Rejected unauthenticated message type %T", msg.Payload)
			sendSessionError(conn, protobuf.ErrorType_SESSION_NOT_LOGGED_ON,
				"session not established — send Logon before any other message")
			closeConn(conn)
			return
		}

		// Authenticated: route normally
		HandleFXPMessage(conn, msg)
	}
}