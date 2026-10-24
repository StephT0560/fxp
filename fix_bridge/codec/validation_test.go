// SPDX-License-Identifier: MIT

package codec

import (
	"bytes"
	"strconv"
	"strings"
	"testing"
)

func validationMessage(t *testing.T, typ string, body ...Field) *Message {
	t.Helper()
	b := NewBuilder(typ, "CLIENT", "FXPBRIDGE", 2)
	for _, f := range body {
		if f.Value != "" {
			b.Append(f.Tag, f.Value)
		}
	}
	m, e := NewParser(bytes.NewReader(b.Build())).ReadMessage()
	if e != nil {
		t.Fatal(e)
	}
	for _, f := range body {
		if f.Value == "" {
			m.Tags = append(m.Tags, f)
			m.Fields[f.Tag] = ""
		}
	}
	return m
}
func validOrder() []Field {
	return []Field{{11, "O1"}, {55, "MSFT"}, {54, "1"}, {60, "20261024-12:00:00"}, {40, "2"}, {38, "10"}, {44, "100.00"}}
}
func TestDictionaryRejectReasons(t *testing.T) {
	for _, tc := range []struct {
		name   string
		fields []Field
		reason string
		tag    int
		strict bool
	}{
		{"missing", validOrder()[1:], "1", 11, false},
		{"undefined", append(validOrder(), Field{4999, "x"}), "0", 4999, false},
		{"wrong message", append(validOrder(), Field{112, "x"}), "2", 112, false},
		{"empty", append(validOrder(), Field{58, ""}), "4", 58, false},
		{"enum", append(validOrder()[:2], append([]Field{{54, "?"}}, validOrder()[3:]...)...), "5", 54, false},
		{"datatype", append(validOrder(), Field{99, "not-a-price"}), "6", 99, false},
		{"duplicate", append(validOrder(), Field{11, "again"}), "13", 11, false},
		{"strict custom", append(validOrder(), Field{9001, "custom"}), "0", 9001, true},
		{"custom default", append(validOrder(), Field{9001, "custom"}), "", 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := validationMessage(t, "D", tc.fields...)
			v := DefaultDictionary.Validate(m, tc.strict)
			if tc.reason == "" {
				if v != nil {
					t.Fatal(v)
				}
				return
			}
			if v == nil || v.Reason != tc.reason || v.Tag != tc.tag {
				t.Fatalf("got %+v want tag %d reason %s", v, tc.tag, tc.reason)
			}
		})
	}
}
func TestDictionaryGroups(t *testing.T) {
	base := []Field{{262, "R1"}, {263, "1"}, {264, "0"}}
	for _, tc := range []struct {
		name   string
		group  []Field
		reason string
	}{
		{"valid", []Field{{267, "2"}, {269, "0"}, {269, "1"}, {146, "1"}, {55, "MSFT"}}, ""},
		{"too few", []Field{{267, "2"}, {269, "0"}, {146, "1"}, {55, "MSFT"}}, "16"},
		{"too many", []Field{{267, "1"}, {269, "0"}, {269, "1"}, {146, "1"}, {55, "MSFT"}}, "16"},
		{"bad order", []Field{{267, "1"}, {269, "0"}, {146, "1"}, {48, "security"}, {55, "MSFT"}}, "15"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := DefaultDictionary.Validate(validationMessage(t, "V", append(base, tc.group...)...), false)
			if tc.reason == "" {
				if v != nil {
					t.Fatal(v)
				}
			} else if v == nil || v.Reason != tc.reason {
				t.Fatalf("got %+v want %s", v, tc.reason)
			}
		})
	}
}
func TestDictionarySupportedMessages(t *testing.T) {
	for typ, body := range map[string][]Field{
		"A": {{98, "0"}, {108, "30"}, {554, "password"}, {8013, "Y"}}, "0": nil, "1": {{112, "REQ"}}, "2": {{7, "1"}, {16, "0"}}, "3": {{45, "1"}}, "4": {{36, "3"}}, "5": nil,
		"D": validOrder(), "F": {{41, "O1"}, {11, "C1"}, {55, "MSFT"}, {54, "1"}, {60, "20261024-12:00:00"}},
		"G": {{41, "O1"}, {11, "R1"}, {55, "MSFT"}, {54, "1"}, {60, "20261024-12:00:00"}, {40, "2"}},
		"V": {{262, "R"}, {263, "0"}, {264, "0"}, {267, "1"}, {269, "0"}, {146, "1"}, {55, "MSFT"}},
		"q": {{11, "M1"}, {530, "7"}, {60, "20261024-12:00:00"}}, "H": {{11, "O1"}, {55, "MSFT"}, {54, "1"}}, "AF": {{584, "M1"}, {585, "7"}},
	} {
		t.Run(typ, func(t *testing.T) {
			if v := DefaultDictionary.Validate(validationMessage(t, typ, body...), false); v != nil {
				t.Fatal(v)
			}
		})
	}
}

func TestParserRetainsEmptyValueForDictionaryReject(t *testing.T) {
	body := strings.Replace(bodyFromWire(goldenVectors[1].wire), "55=MSFT", "55=", 1)
	m, err := NewParser(strings.NewReader(rawWire("FIX.4.4", strconv.Itoa(len(body)), body))).ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	v := DefaultDictionary.Validate(m, false)
	if v == nil || v.Reason != "4" || v.Tag != 55 {
		t.Fatalf("got %+v", v)
	}
}
