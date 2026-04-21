// builder.go — FIX 4.4 message builder
//
// Constructs outbound FIX messages with correct BodyLength and CheckSum.
//
// Usage:
//
//   msg := codec.NewBuilder(codec.MsgTypeExecutionReport, "FXPBRIDGE", "CLIENTFIRM", 42)
//   msg.Set(codec.TagOrderID, "ORD-001")
//   msg.Set(codec.TagExecType, codec.ExecTypeFill)
//   msg.SetInt(codec.TagLastQty, 500)
//   msg.SetFloat(codec.TagLastPx, 185.42, 2)
//   frame := msg.Build()
//
// The caller is responsible for setting application-level fields.
// BodyLength and CheckSum are computed automatically by Build().
//
// Reference: FIX Protocol 4.4 specification
// https://www.fixtrading.org/standards/fix-4-4/

package codec

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Builder constructs a single outbound FIX message.
// Fields are stored in insertion order. Header fields (8, 9, 35, 49, 56, 34, 52)
// are set by NewBuilder; all other fields are set by the caller via Set/SetInt/SetFloat.
type Builder struct {
	fields []Field // insertion-ordered body fields (excludes tag 8, 9, 10)
}

// NewBuilder creates a builder pre-populated with the standard header fields:
//   Tag 35 — MsgType
//   Tag 49 — SenderCompID
//   Tag 56 — TargetCompID
//   Tag 34 — MsgSeqNum
//   Tag 52 — SendingTime (UTC, current time)
func NewBuilder(msgType, senderCompID, targetCompID string, seqNum int) *Builder {
	b := &Builder{}
	b.Set(TagMsgType, msgType)
	b.Set(TagSenderCompID, senderCompID)
	b.Set(TagTargetCompID, targetCompID)
	b.SetInt(TagMsgSeqNum, seqNum)
	b.Set(TagSendingTime, formatUTCTime(time.Now()))
	return b
}

// Set adds or replaces a tag with a string value.
func (b *Builder) Set(tag int, value string) *Builder {
	for i, f := range b.fields {
		if f.Tag == tag {
			b.fields[i].Value = value
			return b
		}
	}
	b.fields = append(b.fields, Field{Tag: tag, Value: value})
	return b
}

// SetInt adds or replaces a tag with an integer value.
func (b *Builder) SetInt(tag int, value int) *Builder {
	return b.Set(tag, strconv.Itoa(value))
}

// SetInt64 adds or replaces a tag with an int64 value.
func (b *Builder) SetInt64(tag int, value int64) *Builder {
	return b.Set(tag, strconv.FormatInt(value, 10))
}

// SetFloat adds or replaces a tag with a float value formatted to `decimals` places.
func (b *Builder) SetFloat(tag int, value float64, decimals int) *Builder {
	return b.Set(tag, strconv.FormatFloat(value, 'f', decimals, 64))
}

// SetPrice sets a price tag from a fixed-point integer (cents).
// e.g. SetPrice(TagLastPx, 18542) → "185.42"
func (b *Builder) SetPrice(tag int, fixedPoint int64) *Builder {
	dollars := fixedPoint / 100
	cents := fixedPoint % 100
	if cents < 0 {
		cents = -cents
	}
	return b.Set(tag, fmt.Sprintf("%d.%02d", dollars, cents))
}

// Build serialises the message into a complete FIX frame (bytes ready to send).
// Computes BodyLength and CheckSum automatically.
// Tag order in the output:
//   8=FIX.4.4 | 9=<bodylen> | <body fields> | 10=<checksum>
func (b *Builder) Build() []byte {
	// Serialise body fields to compute BodyLength
	var body strings.Builder
	for _, f := range b.fields {
		body.WriteString(strconv.Itoa(f.Tag))
		body.WriteByte('=')
		body.WriteString(f.Value)
		body.WriteByte(soh)
	}
	bodyStr := body.String()

	// BodyLength = len("9=<N>\x01") + len(body) + len("10=XXX\x01")
	// where N is the BodyLength value itself. Since N can be 1-6 digits,
	// we compute iteratively (usually converges in 1 pass).
	bodyLen := computeBodyLength(bodyStr)

	// Serialise tag 8 and tag 9
	prefix := fmt.Sprintf("8=FIX.4.4%c9=%d%c", soh, bodyLen, soh)

	// Compute checksum over prefix + body (excluding tag 10 itself)
	var sum int
	for _, b := range []byte(prefix + bodyStr) {
		sum += int(b)
	}
	checksum := sum % 256

	// Assemble final frame
	frame := fmt.Sprintf("%s%s10=%03d%c", prefix, bodyStr, checksum, soh)
	return []byte(frame)
}

// computeBodyLength calculates the BodyLength value.
// BodyLength = bytes from after tag 9's SOH up to and including tag 10's SOH.
// = len(body) + len("10=XXX\x01") where XXX is a 3-digit checksum.
// The tag 9 field itself ("9=N\x01") is NOT included in BodyLength.
func computeBodyLength(bodyStr string) int {
	// "10=XXX\x01" is always 7 bytes (tag 10, equals, 3-digit checksum, SOH)
	const tag10Len = 7 // "10=000\x01"
	return len(bodyStr) + tag10Len
}

// formatUTCTime formats a time as FIX SendingTime: YYYYMMDD-HH:MM:SS.sss
func formatUTCTime(t time.Time) string {
	utc := t.UTC()
	return fmt.Sprintf("%04d%02d%02d-%02d:%02d:%02d.%03d",
		utc.Year(), utc.Month(), utc.Day(),
		utc.Hour(), utc.Minute(), utc.Second(),
		utc.Nanosecond()/1_000_000,
	)
}

// ── Convenience constructors for common message types ─────────────────────────

// NewLogonAck builds a Logon (A) acknowledgement.
func NewLogonAck(senderCompID, targetCompID string, seqNum, heartBtInt int, resetSeqNum bool) []byte {
	b := NewBuilder(MsgTypeLogon, senderCompID, targetCompID, seqNum)
	b.SetInt(TagEncryptMethod, 0)
	b.SetInt(TagHeartBtInt, heartBtInt)
	if resetSeqNum {
		b.Set(TagResetSeqNumFlag, "Y")
	}
	return b.Build()
}

// NewHeartbeat builds a Heartbeat (0) message.
// If testReqID is non-empty it is a response to a TestRequest.
func NewHeartbeat(senderCompID, targetCompID string, seqNum int, testReqID string) []byte {
	b := NewBuilder(MsgTypeHeartbeat, senderCompID, targetCompID, seqNum)
	if testReqID != "" {
		b.Set(TagTestReqID, testReqID)
	}
	return b.Build()
}

// NewLogout builds a Logout (5) message with an optional reason.
func NewLogout(senderCompID, targetCompID string, seqNum int, reason string) []byte {
	b := NewBuilder(MsgTypeLogout, senderCompID, targetCompID, seqNum)
	if reason != "" {
		b.Set(TagText, reason)
	}
	return b.Build()
}

// NewReject builds a session-level Reject (3) message.
func NewReject(senderCompID, targetCompID string, seqNum, refSeqNum int, reason, text string) []byte {
	b := NewBuilder(MsgTypeReject, senderCompID, targetCompID, seqNum)
	b.SetInt(TagRefSeqNum, refSeqNum)
	b.Set(TagSessionRejectReason, reason)
	if text != "" {
		b.Set(TagText, text)
	}
	return b.Build()
}