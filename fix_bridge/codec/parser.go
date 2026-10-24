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
	"errors"
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

// ErrGarbled marks a message that cannot be parsed (bad framing, BodyLength
// or CheckSum). Per FIX it is ignored: the session neither processes it nor
// consumes its sequence number, and the parser resynchronises on the next
// "8=FIX".
var ErrGarbled = errors.New("garbled FIX message")

func garbled(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrGarbled, fmt.Sprintf(format, args...))
}

// Parser reads FIX messages from a buffered reader.
type Parser struct {
	r      *bufio.Reader
	resync bool
}

// NewParser creates a parser reading from r.
func NewParser(r io.Reader) *Parser {
	return &Parser{r: bufio.NewReaderSize(r, 65536)}
}

// ReadMessage reads one complete FIX message from the stream.
// Blocks until a full message is available or an error occurs.
// Returns io.EOF when the connection is closed cleanly.
func (p *Parser) ReadMessage() (*Message, error) {
	if p.resync {
		if err := p.skipToBeginString(); err != nil {
			return nil, err
		}
		p.resync = false
	}
	msg, err := p.readMessage()
	if errors.Is(err, ErrGarbled) {
		p.resync = true
	}
	return msg, err
}

// skipToBeginString discards bytes until the stream is at "8=FIX".
func (p *Parser) skipToBeginString() error {
	for {
		b, err := p.r.Peek(5)
		if err != nil {
			return err
		}
		if string(b) == "8=FIX" {
			return nil
		}
		if _, err := p.r.Discard(1); err != nil {
			return err
		}
	}
}

func (p *Parser) readMessage() (*Message, error) {
	// Step 1: Read tag 8 (BeginString) — must be first
	tag, beginString, rawBeginString, err := p.readField()
	if err != nil {
		return nil, err
	}
	if tag != TagBeginString {
		return nil, garbled("expected tag 8 (BeginString), got tag %d", tag)
	}
	if !strings.HasPrefix(beginString, "FIX.4") {
		return nil, garbled("unsupported BeginString: %q (expected FIX.4.x)", beginString)
	}

	// Step 2: Read tag 9 (BodyLength)
	tag, bodyLengthValue, rawBodyLength, err := p.readField()
	if err != nil {
		return nil, err
	}
	if tag != TagBodyLength {
		return nil, garbled("expected tag 9 (BodyLength), got tag %d", tag)
	}
	bodyLen, err := strconv.Atoi(bodyLengthValue)
	if err != nil {
		return nil, garbled("invalid BodyLength %q: %v", bodyLengthValue, err)
	}
	if bodyLen <= 0 || bodyLen > 1_048_576 {
		return nil, garbled("BodyLength %d out of range", bodyLen)
	}

	// Step 3: Read exactly bodyLen bytes. BodyLength counts from after tag 9's
	// SOH up to and including the SOH immediately before tag 10.
	body := make([]byte, bodyLen)
	if _, err := io.ReadFull(p.r, body); err != nil {
		return nil, fmt.Errorf("reading body: %w", err)
	}
	if body[bodyLen-1] != soh {
		return nil, garbled("incorrect BodyLength %d: body does not end at a field boundary", bodyLen)
	}

	// Step 4: Parse the body into tag=value fields
	fields, err := parseFields(body)
	if err != nil {
		return nil, garbled("parsing body: %v", err)
	}
	for _, f := range fields {
		if f.Tag == TagCheckSum {
			return nil, garbled("incorrect BodyLength %d: CheckSum inside body", bodyLen)
		}
	}

	// The field immediately after the body must be tag 10 (CheckSum).
	tag, checksumValue, _, err := p.readField()
	if err != nil {
		return nil, fmt.Errorf("reading CheckSum: %w", err)
	}
	if tag != TagCheckSum {
		return nil, garbled("incorrect BodyLength %d: expected tag 10 after body, got tag %d", bodyLen, tag)
	}
	if len(checksumValue) != 3 {
		return nil, garbled("CheckSum must be exactly three ASCII digits, got %q", checksumValue)
	}
	for _, b := range []byte(checksumValue) {
		if b < '0' || b > '9' {
			return nil, garbled("CheckSum must be exactly three ASCII digits, got %q", checksumValue)
		}
	}
	expectedChecksum, _ := strconv.Atoi(checksumValue)
	checksumSum := 0
	for _, raw := range [][]byte{[]byte(rawBeginString), []byte(rawBodyLength), body} {
		for _, b := range raw {
			checksumSum += int(b)
		}
	}
	actualChecksum := checksumSum % 256
	if actualChecksum != expectedChecksum {
		return nil, garbled("checksum mismatch: expected %03d, computed %03d", expectedChecksum, actualChecksum)
	}
	fields = append(fields, Field{TagCheckSum, checksumValue})

	// Step 5: Build message — prepend tag 8 and 9
	msg := &Message{
		Tags:   make([]Field, 0, 2+len(fields)),
		Fields: make(map[int]string, 2+len(fields)),
	}
	msg.Tags = append(msg.Tags, Field{TagBeginString, beginString})
	msg.Fields[TagBeginString] = beginString
	msg.Tags = append(msg.Tags, Field{TagBodyLength, bodyLengthValue})
	msg.Fields[TagBodyLength] = bodyLengthValue

	for _, f := range fields {
		msg.Tags = append(msg.Tags, f)
		msg.Fields[f.Tag] = f.Value
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

// readField reads one SOH-terminated "tag=value\x01" pair from the reader.
func (p *Parser) readField() (int, string, string, error) {
	// Read up to SOH
	raw, err := p.r.ReadString(soh)
	if err != nil {
		if err == io.EOF && len(raw) > 0 {
			return 0, "", "", fmt.Errorf("connection closed mid-field")
		}
		return 0, "", "", err
	}

	// Strip trailing SOH
	field := strings.TrimSuffix(raw, string(soh))

	eqIdx := strings.IndexByte(field, '=')
	if eqIdx <= 0 {
		return 0, "", "", garbled("malformed field %q: missing '='", field)
	}

	tag, err := strconv.Atoi(field[:eqIdx])
	if err != nil {
		return 0, "", "", garbled("invalid tag %q: %v", field[:eqIdx], err)
	}

	return tag, field[eqIdx+1:], raw, nil
}
