package codec

import (
	"bufio"
	"bytes"
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
