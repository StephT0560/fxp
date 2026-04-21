// parser.go — FIX 4.4 message parser
//
// FIX wire format:
//   8=FIX.4.4\x01 9=<bodylen>\x01 <body tags> 10=<checksum>\x01
//
// Each tag=value pair is terminated by SOH (\x01).
// BodyLength (tag 9) counts bytes from the character after tag 9's SOH
// up to and including tag 10's SOH.
//
// This parser:
//   1. Reads until it has a complete message (using BodyLength to know when)
//   2. Validates the checksum
//   3. Returns a Message (map of tag → value, plus ordered tag list)
//
// It does NOT validate required fields or field types — that is the
// responsibility of the session and translator layers.

package codec

import (
	"bufio"
	"fmt"
	"io"
	"strconv"
	"strings"
)

const soh = '\x01'

// Message represents a parsed FIX message.
// Tags is an ordered list of (tag, value) pairs — order matters for
// BodyLength calculation and checksum validation.
// Fields is a map for O(1) lookup by tag number.
type Message struct {
	Tags   []Field
	Fields map[int]string
}

// Field is a single tag=value pair in a FIX message.
type Field struct {
	Tag   int
	Value string
}

// Get returns the value of a tag, or "" if not present.
func (m *Message) Get(tag int) string {
	return m.Fields[tag]
}

// GetInt returns the integer value of a tag, or 0 if not present or invalid.
func (m *Message) GetInt(tag int) int {
	v := m.Fields[tag]
	if v == "" {
		return 0
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0
	}
	return n
}

// GetFloat returns the float64 value of a tag, or 0 if not present or invalid.
func (m *Message) GetFloat(tag int) float64 {
	v := m.Fields[tag]
	if v == "" {
		return 0
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return 0
	}
	return f
}

// MsgType returns the message type (tag 35 value).
func (m *Message) MsgType() string {
	return m.Get(TagMsgType)
}

// Parser reads FIX messages from a buffered reader.
type Parser struct {
	r *bufio.Reader
}

// NewParser creates a parser reading from r.
func NewParser(r io.Reader) *Parser {
	return &Parser{r: bufio.NewReaderSize(r, 65536)}
}

// ReadMessage reads one complete FIX message from the stream.
// Blocks until a full message is available or an error occurs.
// Returns io.EOF when the connection is closed cleanly.
func (p *Parser) ReadMessage() (*Message, error) {
	// Step 1: Read tag 8 (BeginString) — must be first
	tag, val, err := p.readField()
	if err != nil {
		return nil, err
	}
	if tag != TagBeginString {
		return nil, fmt.Errorf("expected tag 8 (BeginString), got tag %d", tag)
	}
	if !strings.HasPrefix(val, "FIX.4") {
		return nil, fmt.Errorf("unsupported BeginString: %q (expected FIX.4.x)", val)
	}

	// Step 2: Read tag 9 (BodyLength)
	tag, val, err = p.readField()
	if err != nil {
		return nil, err
	}
	if tag != TagBodyLength {
		return nil, fmt.Errorf("expected tag 9 (BodyLength), got tag %d", tag)
	}
	bodyLen, err := strconv.Atoi(val)
	if err != nil {
		return nil, fmt.Errorf("invalid BodyLength %q: %w", val, err)
	}
	if bodyLen <= 0 || bodyLen > 1_048_576 {
		return nil, fmt.Errorf("BodyLength %d out of range", bodyLen)
	}

	// Step 3: Read exactly bodyLen bytes (the body + tag 10's content)
	// BodyLength counts from after tag 9's SOH up to AND including tag 10's SOH.
	body := make([]byte, bodyLen)
	if _, err := io.ReadFull(p.r, body); err != nil {
		return nil, fmt.Errorf("reading body: %w", err)
	}

	// Step 4: Parse the body into tag=value fields
	// The last field must be tag 10 (CheckSum).
	fields, err := parseFields(body)
	if err != nil {
		return nil, fmt.Errorf("parsing body: %w", err)
	}

	// Step 5: Build message — prepend tag 8 and 9
	msg := &Message{
		Tags:   make([]Field, 0, 2+len(fields)),
		Fields: make(map[int]string, 2+len(fields)),
	}
	msg.Tags = append(msg.Tags, Field{TagBeginString, "FIX.4.4"})
	msg.Fields[TagBeginString] = "FIX.4.4"
	msg.Tags = append(msg.Tags, Field{TagBodyLength, strconv.Itoa(bodyLen)})
	msg.Fields[TagBodyLength] = strconv.Itoa(bodyLen)

	for _, f := range fields {
		msg.Tags = append(msg.Tags, f)
		msg.Fields[f.Tag] = f.Value
	}

	// Step 6: Validate checksum
	if err := validateChecksum(msg); err != nil {
		return nil, err
	}

	return msg, nil
}

// parseFields splits a byte slice of SOH-delimited "tag=value\x01" fields.
func parseFields(data []byte) ([]Field, error) {
	var fields []Field
	remaining := data

	for len(remaining) > 0 {
		// Find next SOH
		idx := -1
		for i, b := range remaining {
			if b == soh {
				idx = i
				break
			}
		}
		if idx == -1 {
			return nil, fmt.Errorf("missing SOH delimiter in body")
		}

		pair := string(remaining[:idx])
		remaining = remaining[idx+1:]

		if pair == "" {
			continue
		}

		eqIdx := strings.IndexByte(pair, '=')
		if eqIdx <= 0 {
			return nil, fmt.Errorf("malformed field %q: missing '='", pair)
		}

		tagStr := pair[:eqIdx]
		value := pair[eqIdx+1:]

		tag, err := strconv.Atoi(tagStr)
		if err != nil {
			return nil, fmt.Errorf("invalid tag number %q: %w", tagStr, err)
		}

		fields = append(fields, Field{Tag: tag, Value: value})
	}

	return fields, nil
}

// validateChecksum verifies tag 10 against the sum of all preceding bytes.
// FIX checksum = sum of ASCII values of all bytes before tag 10, mod 256.
// Tag 10 must be the last field.
func validateChecksum(msg *Message) error {
	checksumStr := msg.Get(TagCheckSum)
	if checksumStr == "" {
		return fmt.Errorf("missing CheckSum (tag 10)")
	}
	expected, err := strconv.Atoi(checksumStr)
	if err != nil {
		return fmt.Errorf("invalid CheckSum value %q", checksumStr)
	}

	// Recompute checksum over all bytes except tag 10 itself
	var sum int
	for _, f := range msg.Tags {
		if f.Tag == TagCheckSum {
			break
		}
		sum += checksumBytes(f.Tag, f.Value)
	}
	actual := sum % 256

	if actual != expected {
		return fmt.Errorf("checksum mismatch: expected %03d, computed %03d", expected, actual)
	}
	return nil
}

// checksumBytes computes the byte sum of "TAG=VALUE\x01"
func checksumBytes(tag int, value string) int {
	s := strconv.Itoa(tag) + "=" + value + string(soh)
	var sum int
	for _, b := range []byte(s) {
		sum += int(b)
	}
	return sum
}

// readField reads one SOH-terminated "tag=value\x01" pair from the reader.
func (p *Parser) readField() (int, string, error) {
	// Read up to SOH
	s, err := p.r.ReadString(soh)
	if err != nil {
		if err == io.EOF && len(s) > 0 {
			return 0, "", fmt.Errorf("connection closed mid-field")
		}
		return 0, "", err
	}

	// Strip trailing SOH
	s = strings.TrimSuffix(s, string(soh))

	eqIdx := strings.IndexByte(s, '=')
	if eqIdx <= 0 {
		return 0, "", fmt.Errorf("malformed field %q: missing '='", s)
	}

	tag, err := strconv.Atoi(s[:eqIdx])
	if err != nil {
		return 0, "", fmt.Errorf("invalid tag %q: %w", s[:eqIdx], err)
	}

	return tag, s[eqIdx+1:], nil
}