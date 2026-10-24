// SPDX-License-Identifier: MIT

package transport

// auth.go — JWT token validation and session lifecycle for the FXP gateway.
//
// Design:
//   - Every connection must send a Logon message as its FIRST message.
//   - The Logon.token field must be a valid HS256-signed JWT.
//   - The header alg must be HS256; an optional kid selects the current or
//     previous key (key rotation).
//   - Required JWT claims: sub (trader ID), exp, iat, iss and aud; nbf is
//     checked when present; jti is required when the replay cache is on.
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
	"container/heap"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"go_gateway/protobuf"

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

// sessionCOD holds the sessions that asked for cancel-on-disconnect at Logon.
var sessionCOD sync.Map // session_id (string) → struct{}

// endSession ends a session because its client logged out or disconnected.
// For a cancel-on-disconnect session the core cancels every live order of
// the trader, including parked stops.
func endSession(sessionID string) {
	traderID := GetTraderID(sessionID)
	_, cod := sessionCOD.LoadAndDelete(sessionID)
	invalidateSession(sessionID)
	if cod && traderID != "" {
		cancelOnDisconnect(traderID)
	}
}

// cancelOnDisconnect sends the internal owner-scoped mass cancel.
var cancelOnDisconnect = func(traderID string) {
	msg := &protobuf.FXPMessage{Payload: &protobuf.FXPMessage_OrderMassCancel{
		OrderMassCancel: &protobuf.OrderMassCancelRequest{
			Sender:    traderID,
			Reason:    protobuf.MassCancelReason_MASS_CANCEL_REASON_DISCONNECT,
			Timestamp: timestamppb.Now(),
		},
	}}
	raw, err := proto.Marshal(msg)
	if err != nil {
		log.Printf("❌ cancel-on-disconnect encode failed for %s: %v", traderID, err)
		return
	}
	if err := writeToCore(frameBytes(raw)); err != nil {
		log.Printf("❌ cancel-on-disconnect for %s not sent: %v", traderID, err)
		return
	}
	log.Printf("🧹 cancel-on-disconnect sent for trader %s", traderID)
}

// invalidateSession removes a session (called on Logout or disconnect).
func invalidateSession(sessionID string) {
	sessionStore.Delete(sessionID)
	log.Printf("🔒 Session invalidated: %s", sessionID)
}

// ── JWT validation ────────────────────────────────────────────────────────────

// Dev defaults for the issuer and audience the gateway accepts. Production
// deployments set FXP_JWT_ISSUER / FXP_JWT_AUDIENCE to their identity
// provider's values; the bridge and test clients mint with the same env vars.
const (
	DefaultJWTIssuer   = "fxp-dev"
	DefaultJWTAudience = "fxp-gateway"
)

type jwtHeader struct {
	Alg string `json:"alg"`
	Kid string `json:"kid"`
}

type jwtClaims struct {
	Sub string      `json:"sub"` // trader ID
	Exp int64       `json:"exp"` // unix expiry
	Iat int64       `json:"iat"` // unix issued-at
	Nbf int64       `json:"nbf"` // unix not-before (optional)
	Iss string      `json:"iss"`
	Aud jwtAudience `json:"aud"`
	Jti string      `json:"jti"` // token ID (required when the replay cache is on)
}

// jwtAudience accepts both forms RFC 7519 allows: a string or an array.
type jwtAudience []string

func (a *jwtAudience) UnmarshalJSON(b []byte) error {
	var one string
	if err := json.Unmarshal(b, &one); err == nil {
		*a = jwtAudience{one}
		return nil
	}
	var many []string
	if err := json.Unmarshal(b, &many); err != nil {
		return fmt.Errorf("aud must be a string or an array of strings")
	}
	*a = many
	return nil
}

func (a jwtAudience) contains(want string) bool {
	for _, v := range a {
		if v == want {
			return true
		}
	}
	return false
}

// jwtConfig is read from the environment on every validation so rotations
// and tests take effect without a restart.
type jwtConfig struct {
	currentKid string
	keys       map[string][]byte // kid → HS256 secret; "" is the current key
	issuer     string
	audience   string
	leeway     int64 // seconds of clock skew tolerated on nbf
	replay     bool
}

// loadJWTConfig reads:
//
//	FXP_JWT_SECRET            current HS256 key (required)
//	FXP_JWT_KID               kid of the current key (optional)
//	FXP_JWT_PREVIOUS_KID      kid of the previous key, still accepted during rotation
//	FXP_JWT_PREVIOUS_SECRET   previous key (set together with FXP_JWT_PREVIOUS_KID)
//	FXP_JWT_ISSUER            required iss (default fxp-dev)
//	FXP_JWT_AUDIENCE          required aud (default fxp-gateway)
//	FXP_JWT_LEEWAY_SEC        nbf clock-skew leeway (default 30)
//	FXP_JWT_REPLAY_CACHE      "on" rejects a reused jti until it expires (default off)
func loadJWTConfig() jwtConfig {
	cfg := jwtConfig{
		currentKid: os.Getenv("FXP_JWT_KID"),
		keys:       map[string][]byte{"": jwtSecret()},
		issuer:     envOr("FXP_JWT_ISSUER", DefaultJWTIssuer),
		audience:   envOr("FXP_JWT_AUDIENCE", DefaultJWTAudience),
		leeway:     int64(envInt("FXP_JWT_LEEWAY_SEC", 30)),
		replay:     envBool("FXP_JWT_REPLAY_CACHE"),
	}
	if cfg.currentKid != "" {
		cfg.keys[cfg.currentKid] = cfg.keys[""]
	}
	pk, ps := os.Getenv("FXP_JWT_PREVIOUS_KID"), os.Getenv("FXP_JWT_PREVIOUS_SECRET")
	switch {
	case pk != "" && ps != "" && pk != cfg.currentKid:
		cfg.keys[pk] = []byte(ps)
	case pk != "" || ps != "":
		log.Printf("⚠️ ignoring FXP_JWT_PREVIOUS_KID/FXP_JWT_PREVIOUS_SECRET: both must be set and the kid must differ from FXP_JWT_KID")
	}
	return cfg
}

func envOr(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

func envBool(name string) bool {
	switch strings.ToLower(os.Getenv(name)) {
	case "1", "on", "true", "yes":
		return true
	}
	return false
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
// The header must declare alg HS256 (none and every other algorithm are
// rejected before the MAC is checked) and, if it carries a kid, name a
// configured key. The claims must carry sub, exp, iat and the configured
// iss and aud; nbf is honoured with a small leeway.
func validateJWT(token string) (*jwtClaims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, fmt.Errorf("malformed JWT: expected 3 parts, got %d", len(parts))
	}
	cfg := loadJWTConfig()

	headerBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, fmt.Errorf("JWT header decode failed: %w", err)
	}
	var hdr jwtHeader
	if err := json.Unmarshal(headerBytes, &hdr); err != nil {
		return nil, fmt.Errorf("JWT header unmarshal failed: %w", err)
	}
	if hdr.Alg != "HS256" {
		return nil, fmt.Errorf("JWT alg %q not allowed: only HS256 is accepted", hdr.Alg)
	}
	key, ok := cfg.keys[hdr.Kid]
	if !ok {
		return nil, fmt.Errorf("JWT kid %q is not a configured key", hdr.Kid)
	}

	// Verify signature: HMAC-SHA256(base64url(header) + "." + base64url(payload))
	signingInput := parts[0] + "." + parts[1]
	mac := hmac.New(sha256.New, key)
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
	if claims.Iss != cfg.issuer {
		return nil, fmt.Errorf("JWT iss %q does not match the configured issuer", claims.Iss)
	}
	if !claims.Aud.contains(cfg.audience) {
		return nil, fmt.Errorf("JWT aud %q does not include the configured audience", []string(claims.Aud))
	}
	now := time.Now().Unix()
	if now > claims.Exp {
		return nil, fmt.Errorf("JWT expired at %d", claims.Exp)
	}
	if claims.Nbf != 0 && now+cfg.leeway < claims.Nbf {
		return nil, fmt.Errorf("JWT not valid before %d (nbf)", claims.Nbf)
	}
	if cfg.replay {
		if claims.Jti == "" {
			return nil, fmt.Errorf("JWT missing required claim: jti (replay cache is on)")
		}
		if !replayCache.firstUse(jtiKey{claims.Iss, hdr.Kid, claims.Jti}, claims.Exp, now) {
			return nil, fmt.Errorf("JWT replay: jti %q was already used", claims.Jti)
		}
	}

	return &claims, nil
}

// jtiCache remembers token IDs until their tokens expire. seen gives O(1)
// duplicate checks; byExp is a min-heap on exp so pruning only touches
// entries that have actually expired.
type jtiCache struct {
	mu    sync.Mutex
	seen  map[jtiKey]int64 // → exp
	byExp jtiHeap
}

// jtiKey scopes a jti to its issuer and signing key: RFC 7519 only requires
// jti to be unique per issuer, and tokens under different keys are different
// tokens. A struct (not a joined string) so no jti can collide across fields.
type jtiKey struct{ iss, kid, jti string }

type jtiEntry struct {
	key jtiKey
	exp int64
}

type jtiHeap []jtiEntry

func (h jtiHeap) Len() int           { return len(h) }
func (h jtiHeap) Less(i, j int) bool { return h[i].exp < h[j].exp }
func (h jtiHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *jtiHeap) Push(x any)        { *h = append(*h, x.(jtiEntry)) }
func (h *jtiHeap) Pop() any {
	old := *h
	e := old[len(old)-1]
	*h = old[:len(old)-1]
	return e
}

var replayCache = &jtiCache{seen: map[jtiKey]int64{}}

func resetReplayCache() {
	replayCache.mu.Lock()
	replayCache.seen = map[jtiKey]int64{}
	replayCache.byExp = nil
	replayCache.mu.Unlock()
}

// firstUse records key and reports whether it had not been seen before.
func (c *jtiCache) firstUse(key jtiKey, exp, now int64) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for c.byExp.Len() > 0 && c.byExp[0].exp < now {
		e := heap.Pop(&c.byExp).(jtiEntry)
		if c.seen[e.key] == e.exp {
			delete(c.seen, e.key)
		}
	}
	if _, dup := c.seen[key]; dup {
		return false
	}
	c.seen[key] = exp
	heap.Push(&c.byExp, jtiEntry{key, exp})
	return true
}

// ── Logon handling ────────────────────────────────────────────────────────────

// HandleLogon validates the Logon token and registers a session under a
// server-generated id; the client-supplied Logon.session_id is not trusted.
// The caller is responsible for closing the connection on failure.
func HandleLogon(logon *protobuf.Logon) (traderID, sessionID string, err error) {
	claims, err := validateJWT(logon.Token)
	if err != nil {
		return "", "", fmt.Errorf("token validation failed: %w", err)
	}

	sessionID = newSessionID()
	sessionStore.Store(sessionID, claims.Sub)
	if logon.CancelOnDisconnect {
		sessionCOD.Store(sessionID, struct{}{})
	}
	log.Printf("✅ Logon accepted: session=%s trader=%s", sessionID, claims.Sub)

	return claims.Sub, sessionID, nil
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

	if err := writeFXPMessage(conn, ack); err != nil {
		log.Printf("❌ Failed to send LogonAck: %v", err)
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

	if err := writeFXPMessage(conn, errMsg); err != nil {
		log.Printf("❌ Failed to send ErrorMessage: %v", err)
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

		traderID, newID, err := HandleLogon(logon)
		if err != nil {
			log.Printf("❌ Logon rejected: %v", err)
			sendLogonAck(conn, false, err.Error())
			drainTerminalResponse(conn)
			return
		}

		// A repeated Logon replaces this connection's previous session; the
		// connection stays up, so this is not a disconnect.
		if *sessionID != "" {
			sessionCOD.Delete(*sessionID)
			invalidateSession(*sessionID)
		}
		*sessionID = newID
		sendLogonAck(conn, true, "")
		log.Printf("🔓 Session %s authenticated as trader %s", newID, traderID)

	case *protobuf.FXPMessage_Logout:
		// Logout is allowed whether authenticated or not
		if *sessionID != "" {
			endSession(*sessionID)
			*sessionID = ""
		}
		closeConn(conn)

	default:
		// All other message types require an active session
		if *sessionID == "" || !IsAuthenticated(*sessionID) {
			log.Printf("⛔ Rejected unauthenticated message type %T", msg.Payload)
			sendSessionError(conn, protobuf.ErrorType_SESSION_NOT_LOGGED_ON,
				"session not established — send Logon before any other message")
			drainTerminalResponse(conn)
			return
		}

		// Authenticated: bind order-entry messages to the session's trader, then route
		bindOwner(msg, GetTraderID(*sessionID))
		HandleFXPMessage(conn, msg)
	}
}

func drainTerminalResponse(conn any) {
	value, ok := clientWriters.Load(conn)
	if !ok {
		closeConn(conn)
		return
	}
	writer := value.(*clientWriter)
	writer.drain()
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	select {
	case <-writer.finished:
	case <-timer.C:
		writer.forceStop()
	}
}
