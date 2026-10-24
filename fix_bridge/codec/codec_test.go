// SPDX-License-Identifier: MIT

package codec

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"
)

// Golden vectors computed by hand (outside this package) per FIX 4.4:
// BodyLength(9) = byte count from the first byte after the SOH that ends
// tag 9 up to and including the SOH immediately before tag 10.
// CheckSum(10) = sum of all bytes before "10=" mod 256.
var goldenVectors = []struct {
	name    string
	bodyLen int
	fields  []Field // body fields in wire order (tags 35..last)
	wire    string
}{
	{
		name:    "Logon",
		bodyLen: 74,
		fields:  []Field{{35, "A"}, {49, "TESTFIRM01"}, {56, "FXPBRIDGE"}, {34, "1"}, {52, "20261024-03:38:00.000"}, {98, "0"}, {108, "30"}},
		wire:    "8=FIX.4.4\x019=74\x0135=A\x0149=TESTFIRM01\x0156=FXPBRIDGE\x0134=1\x0152=20261024-03:38:00.000\x0198=0\x01108=30\x0110=090\x01",
	},
	{
		name:    "NewOrderSingle",
		bodyLen: 136,
		fields:  []Field{{35, "D"}, {49, "TESTFIRM01"}, {56, "FXPBRIDGE"}, {34, "2"}, {52, "20261024-03:38:00.000"}, {11, "ORD-1"}, {55, "MSFT"}, {54, "1"}, {38, "100"}, {40, "2"}, {59, "0"}, {60, "20261024-03:38:00.000"}, {44, "150.25"}},
		wire:    "8=FIX.4.4\x019=136\x0135=D\x0149=TESTFIRM01\x0156=FXPBRIDGE\x0134=2\x0152=20261024-03:38:00.000\x0111=ORD-1\x0155=MSFT\x0154=1\x0138=100\x0140=2\x0159=0\x0160=20261024-03:38:00.000\x0144=150.25\x0110=142\x01",
	},
	{
		name:    "ExecutionReport",
		bodyLen: 147,
		fields:  []Field{{35, "8"}, {49, "FXPBRIDGE"}, {56, "TESTFIRM01"}, {34, "2"}, {52, "20261024-03:38:00.000"}, {37, "ORD-1"}, {17, "EXEC-1"}, {150, "0"}, {39, "0"}, {55, "MSFT"}, {11, "ORD-1"}, {151, "100"}, {14, "0"}, {60, "20261024-03:38:00.000"}},
		wire:    "8=FIX.4.4\x019=147\x0135=8\x0149=FXPBRIDGE\x0156=TESTFIRM01\x0134=2\x0152=20261024-03:38:00.000\x0137=ORD-1\x0117=EXEC-1\x01150=0\x0139=0\x0155=MSFT\x0111=ORD-1\x01151=100\x0114=0\x0160=20261024-03:38:00.000\x0110=028\x01",
	},
}

func TestBuilderGoldenVectors(t *testing.T) {
	for _, gv := range goldenVectors {
		f := gv.fields
		b := NewBuilder(f[0].Value, f[1].Value, f[2].Value, mustAtoi(t, f[3].Value))
		for _, fld := range f[4:] {
			b.Set(fld.Tag, fld.Value)
		}
		if got := string(b.Build()); got != gv.wire {
			t.Errorf("%s:\n got  %q\n want %q", gv.name, got, gv.wire)
		}
	}
}

func TestParserGoldenVectors(t *testing.T) {
	for _, gv := range goldenVectors {
		msg, err := NewParser(strings.NewReader(gv.wire)).ReadMessage()
		if err != nil {
			t.Fatalf("%s: %v", gv.name, err)
		}
		if got := msg.GetInt(TagBodyLength); got != gv.bodyLen {
			t.Errorf("%s: BodyLength = %d, want %d", gv.name, got, gv.bodyLen)
		}
		for _, fld := range gv.fields {
			if got := msg.Get(fld.Tag); got != fld.Value {
				t.Errorf("%s: tag %d = %q, want %q", gv.name, fld.Tag, got, fld.Value)
			}
		}
	}
}

// Back-to-back golden messages must parse without losing frame alignment.
func TestParserStreamOfGoldenVectors(t *testing.T) {
	var stream bytes.Buffer
	for _, gv := range goldenVectors {
		stream.WriteString(gv.wire)
	}
	p := NewParser(bufio.NewReader(&stream))
	for _, gv := range goldenVectors {
		msg, err := p.ReadMessage()
		if err != nil {
			t.Fatalf("%s: %v", gv.name, err)
		}
		if msg.MsgType() != gv.fields[0].Value {
			t.Fatalf("%s: MsgType = %q", gv.name, msg.MsgType())
		}
	}
}

// Messages whose BodyLength does not end exactly at tag 10 are rejected,
// including the old "+7" off-by-tag-10 encoding.
func TestParserRejectsWrongBodyLength(t *testing.T) {
	for _, gv := range goldenVectors {
		for _, delta := range []int{-1, 1, 7} {
			bad := withBodyLength(gv.wire, gv.bodyLen+delta)
			if _, err := NewParser(strings.NewReader(bad + gv.wire)).ReadMessage(); err == nil {
				t.Errorf("%s: BodyLength %+d accepted", gv.name, delta)
			}
		}
	}
}

func TestParserPreservesReceivedBodyLength(t *testing.T) {
	body := bodyFromWire(goldenVectors[0].wire)
	bodyLength := fmt.Sprintf("%03d", len(body))
	wire := rawWire("FIX.4.4", bodyLength, body)

	msg, err := NewParser(strings.NewReader(wire)).ReadMessage()
	if err != nil {
		t.Fatalf("zero-padded BodyLength rejected: %v", err)
	}
	if got := msg.Get(TagBodyLength); got != bodyLength {
		t.Fatalf("BodyLength = %q, want received value %q", got, bodyLength)
	}
}

// Only the exact configured BeginString is accepted. The message is still
// returned so the session can answer with a Logout.
func TestParserRejectsOtherBeginString(t *testing.T) {
	body := bodyFromWire(goldenVectors[0].wire)
	for _, bs := range []string{"FIX.4.2", "FIX.4.4.1", "FIX.4", "FIXT.1.1"} {
		t.Run(bs, func(t *testing.T) {
			p := NewParser(strings.NewReader(rawWire(bs, strconv.Itoa(len(body)), body) + goldenVectors[1].wire))
			msg, err := p.ReadMessage()
			if !errors.Is(err, ErrBeginString) {
				t.Fatalf("ReadMessage error = %v, want ErrBeginString", err)
			}
			if msg == nil || msg.Get(TagBeginString) != bs {
				t.Fatalf("mismatched message not returned with BeginString %q: %+v", bs, msg)
			}
			if next, err := p.ReadMessage(); err != nil || next.MsgType() != MsgTypeNewOrderSingle {
				t.Fatalf("next message after BeginString mismatch: %v", err)
			}
		})
	}
}

func TestParserSetBeginString(t *testing.T) {
	body := bodyFromWire(goldenVectors[0].wire)
	p := NewParser(strings.NewReader(rawWire("FIX.4.2", strconv.Itoa(len(body)), body) + goldenVectors[0].wire))
	p.SetBeginString("FIX.4.2")
	if _, err := p.ReadMessage(); err != nil {
		t.Fatalf("configured FIX.4.2 rejected: %v", err)
	}
	if _, err := p.ReadMessage(); !errors.Is(err, ErrBeginString) {
		t.Fatalf("FIX.4.4 on a FIX.4.2 parser: err = %v, want ErrBeginString", err)
	}
}

func TestParserRejectsNonFIXBeginStringAsGarbled(t *testing.T) {
	body := bodyFromWire(goldenVectors[0].wire)
	wire := rawWire("XYZ.1.0", strconv.Itoa(len(body)), body)
	if _, err := NewParser(strings.NewReader(wire)).ReadMessage(); !errors.Is(err, ErrGarbled) {
		t.Fatalf("ReadMessage error = %v, want ErrGarbled", err)
	}
}

func TestParserRejectsEmptyField(t *testing.T) {
	body := strings.Replace(bodyFromWire(goldenVectors[1].wire), "\x0155=MSFT\x01", "\x0155=MSFT\x01\x01", 1)
	wire := rawWire("FIX.4.4", strconv.Itoa(len(body)), body)
	p := NewParser(strings.NewReader(wire + goldenVectors[0].wire))
	if _, err := p.ReadMessage(); !errors.Is(err, ErrGarbled) {
		t.Fatalf("empty field (SOH SOH): err = %v, want ErrGarbled", err)
	}
	if msg, err := p.ReadMessage(); err != nil || msg.MsgType() != MsgTypeLogon {
		t.Fatalf("parser did not resync after empty field: %v", err)
	}
}

func TestParserRejectsMalformedFields(t *testing.T) {
	tests := []struct {
		name  string
		field string
	}{
		{"missing equals", "55MSFT"},
		{"empty tag", "=MSFT"},
		{"empty value", "55="},
		{"non-numeric tag", "abc=1"},
		{"leading zero tag", "055=MSFT"},
		{"signed tag", "+55=MSFT"},
		{"negative tag", "-55=MSFT"},
		{"zero tag", "0=MSFT"},
		{"space in tag", " 55=MSFT"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := strings.Replace(bodyFromWire(goldenVectors[1].wire), "55=MSFT", tt.field, 1)
			wire := rawWire("FIX.4.4", strconv.Itoa(len(body)), body)
			if _, err := NewParser(strings.NewReader(wire)).ReadMessage(); !errors.Is(err, ErrGarbled) {
				t.Fatalf("field %q: err = %v, want ErrGarbled", tt.field, err)
			}
		})
	}
}

func TestParserRejectsMalformedHeaderFields(t *testing.T) {
	body := bodyFromWire(goldenVectors[0].wire)
	for name, wire := range map[string]string{
		"leading zero tag 8": "0" + rawWire("FIX.4.4", strconv.Itoa(len(body)), body),
		"empty BodyLength":   "8=FIX.4.4\x019=\x01" + body + "10=000\x01",
		"signed BodyLength":  rawWire("FIX.4.4", "+"+strconv.Itoa(len(body)), body),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := NewParser(strings.NewReader(wire)).ReadMessage(); !errors.Is(err, ErrGarbled) {
				t.Fatalf("err = %v, want ErrGarbled", err)
			}
		})
	}
}

// FIX forbids empty values, so the builder omits unset fields rather than
// emitting "tag=" that a strict peer (or our own resend path) would reject.
func TestBuilderOmitsEmptyValues(t *testing.T) {
	b := NewBuilder(MsgTypeExecutionReport, "FXPBRIDGE", "CLIENT", 1)
	b.Set(TagClOrdID, "")
	b.Set(TagSymbol, "MSFT")
	b.Append(TagText, "")
	msg, err := NewParser(bytes.NewReader(b.Build())).ReadMessage()
	if err != nil {
		t.Fatalf("message with empty Set values does not parse: %v", err)
	}
	if _, ok := msg.Fields[TagClOrdID]; ok {
		t.Fatal("empty ClOrdID emitted")
	}
	if _, ok := msg.Fields[TagText]; ok {
		t.Fatal("empty Text emitted")
	}
	if msg.Get(TagSymbol) != "MSFT" {
		t.Fatal("Symbol lost")
	}
}

func TestParserRejectsMalformedChecksumFields(t *testing.T) {
	base := wireWithChecksumValue(t, 7)
	original := base[strings.LastIndex(base, "10=")+3 : strings.LastIndex(base, string(soh))]
	wrong := fmt.Sprintf("%03d", (mustAtoi(t, original)+1)%256)
	tests := []struct {
		name     string
		checksum string
	}{
		{name: "one digit", checksum: "7"},
		{name: "four digits", checksum: "0007"},
		{name: "non-digit", checksum: "abc"},
		{name: "wrong checksum", checksum: wrong},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wire := replaceChecksum(base, tt.checksum)
			if _, err := NewParser(strings.NewReader(wire)).ReadMessage(); !errors.Is(err, ErrGarbled) {
				t.Fatalf("ReadMessage error = %v, want ErrGarbled", err)
			}
		})
	}
}

func TestParserLengthPrefixedDataWithEmbeddedSOH(t *testing.T) {
	data := "A\x01B=C"
	builder := NewBuilder(MsgTypeNewOrderSingle, "CLIENT", "FXPBRIDGE", 1)
	builder.Set(95, strconv.Itoa(len(data)))
	builder.Set(96, data)
	first := builder.Build()
	second := NewBuilder(MsgTypeHeartbeat, "CLIENT", "FXPBRIDGE", 2).Build()
	stream := append(append([]byte{}, first...), second...)

	parser := NewParser(bytes.NewReader(stream))
	msg, err := parser.ReadMessage()
	if err != nil {
		t.Fatalf("message with RawData rejected: %v", err)
	}
	if got := msg.Get(96); got != data {
		t.Fatalf("RawData = %q, want %q", got, data)
	}
	msg, err = parser.ReadMessage()
	if err != nil {
		t.Fatalf("second message after RawData: %v", err)
	}
	if got := msg.MsgType(); got != MsgTypeHeartbeat {
		t.Fatalf("second MsgType = %q, want %q", got, MsgTypeHeartbeat)
	}
}

func TestParserRejectsWrongLengthAndResyncs(t *testing.T) {
	data := "ABCDE"
	builder := NewBuilder(MsgTypeNewOrderSingle, "CLIENT", "FXPBRIDGE", 1)
	builder.Set(95, strconv.Itoa(len(data)-1))
	builder.Set(96, data)
	bad := builder.Build()
	good := NewBuilder(MsgTypeHeartbeat, "CLIENT", "FXPBRIDGE", 2).Build()
	parser := NewParser(bytes.NewReader(append(append([]byte{}, bad...), good...)))

	if _, err := parser.ReadMessage(); !errors.Is(err, ErrGarbled) {
		t.Fatalf("wrong data length error = %v, want ErrGarbled", err)
	}
	msg, err := parser.ReadMessage()
	if err != nil {
		t.Fatalf("parser did not resync after wrong data length: %v", err)
	}
	if got := msg.MsgType(); got != MsgTypeHeartbeat {
		t.Fatalf("resynced MsgType = %q, want %q", got, MsgTypeHeartbeat)
	}
}

// §3.6 sample: LogonAck FXPBRIDGE -> VICTIM, seq 1, HeartBtInt 30:
// 35=A(5) 49=FXPBRIDGE(13) 56=VICTIM(10) 34=1(5) 52=<21 chars>(25) 98=0(5) 108=30(7) = 70.
func TestLogonAckBodyLength(t *testing.T) {
	ack := string(NewLogonAck("FXPBRIDGE", "VICTIM", 1, 30, false))
	if !strings.HasPrefix(ack, "8=FIX.4.4\x019=70\x01") {
		t.Fatalf("LogonAck BodyLength must be 70: %q", ack)
	}
	if _, err := NewParser(strings.NewReader(ack)).ReadMessage(); err != nil {
		t.Fatalf("LogonAck does not parse: %v", err)
	}
}

// withBodyLength rewrites tag 9 and recomputes the checksum so that only the
// BodyLength is wrong.
func withBodyLength(wire string, n int) string {
	rest := wire[strings.Index(wire, "\x0135=")+1 : strings.LastIndex(wire, "10=")]
	prefix := "8=FIX.4.4\x019=" + strconv.Itoa(n) + "\x01"
	sum := 0
	for _, c := range []byte(prefix + rest) {
		sum += int(c)
	}
	return prefix + rest + "10=" + leftPad3(sum%256) + "\x01"
}

func bodyFromWire(wire string) string {
	fields := strings.Split(wire, string(soh))
	return strings.Join(fields[2:len(fields)-2], string(soh)) + string(soh)
}

func rawWire(beginString, bodyLength, body string) string {
	prefix := "8=" + beginString + string(soh) + "9=" + bodyLength + string(soh)
	sum := 0
	for _, b := range []byte(prefix + body) {
		sum += int(b)
	}
	return prefix + body + fmt.Sprintf("10=%03d%c", sum%256, soh)
}

func wireWithChecksumValue(t *testing.T, want int) string {
	t.Helper()
	bodyPrefix := "35=A\x0149=CLIENT\x0156=FXPBRIDGE\x0134=1\x0152=20261024-03:38:00.000\x01"
	for n := 0; n < 512; n++ {
		body := bodyPrefix + "58=" + strings.Repeat("A", n) + string(soh)
		wire := rawWire("FIX.4.4", strconv.Itoa(len(body)), body)
		value := wire[strings.LastIndex(wire, "10=")+3 : strings.LastIndex(wire, string(soh))]
		if got, _ := strconv.Atoi(value); got == want {
			return wire
		}
	}
	t.Fatalf("could not construct message with checksum %03d", want)
	return ""
}

func replaceChecksum(wire, checksum string) string {
	start := strings.LastIndex(wire, "10=")
	return wire[:start] + "10=" + checksum + string(soh)
}

func leftPad3(n int) string {
	s := strconv.Itoa(n)
	for len(s) < 3 {
		s = "0" + s
	}
	return s
}

func mustAtoi(t *testing.T, s string) int {
	t.Helper()
	n, err := strconv.Atoi(s)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestRepeatingGroupRoundTrip(t *testing.T) {
	b := NewBuilder(MsgTypeMarketDataIncremental, "FXPBRIDGE", "CLIENT", 7)
	b.Set(TagMDReqID, "REQ")
	b.SetInt(TagNoMDEntries, 3)
	for i := 0; i < 3; i++ {
		b.Append(TagMDUpdateAction, "0")
		b.Append(TagMDEntryPx, strconv.Itoa(100+i))
	}
	m, err := NewParser(bytes.NewReader(b.Build())).ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	var px []string
	for _, f := range m.Tags {
		if f.Tag == TagMDEntryPx {
			px = append(px, f.Value)
		}
	}
	if strings.Join(px, ",") != "100,101,102" || m.Get(TagNoMDEntries) != "3" {
		t.Fatalf("270 values %v, 268=%s; want 100,101,102 and 3", px, m.Get(TagNoMDEntries))
	}
}
