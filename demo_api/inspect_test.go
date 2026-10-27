// SPDX-License-Identifier: MIT
package main

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"go_gateway/protobuf"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

func TestInspectFIXGoldens(t *testing.T) {
	data, e := os.ReadFile("../fix_bridge/translator/testdata/order_preview.json")
	if e != nil {
		t.Fatal(e)
	}
	var fixtures []struct {
		Name  string
		Order json.RawMessage
		FIX   string
	}
	if e = json.Unmarshal(data, &fixtures); e != nil {
		t.Fatal(e)
	}
	for _, f := range fixtures {
		t.Run(f.Name, func(t *testing.T) {
			o := new(protobuf.NewOrderSingle)
			if e := protojson.Unmarshal(f.Order, o); e != nil {
				t.Fatal(e)
			}
			raw, e := proto.Marshal(&protobuf.FXPMessage{Payload: &protobuf.FXPMessage_NewOrder{NewOrder: o}})
			if e != nil {
				t.Fatal(e)
			}
			body, _ := json.Marshal(map[string]string{"frame": base64.StdEncoding.EncodeToString(raw)})
			w := httptest.NewRecorder()
			r := httptest.NewRequest("POST", "/api/inspect/fix", strings.NewReader(string(body)))
			r.Header.Set("Content-Type", "application/json")
			(&app{}).inspectFIX(w, r)
			if w.Code != http.StatusOK {
				t.Fatalf("%d %s", w.Code, w.Body)
			}
			var got struct {
				FIX      string `json:"fix"`
				FIXBytes int    `json:"fix_bytes"`
				FXPBytes int    `json:"fxp_bytes"`
			}
			if e := json.Unmarshal(w.Body.Bytes(), &got); e != nil {
				t.Fatal(e)
			}
			if got.FIX != f.FIX || got.FIXBytes != len(f.FIX) || got.FXPBytes != len(raw) {
				t.Fatalf("unexpected comparison: %+v", got)
			}
		})
	}
}
func TestInspectRejectsBadInput(t *testing.T) {
	for _, body := range []string{`{}`, `{"frame":"not base64"}`, `{"frame":"CgA="}`, `{"frame":"","order":{}}`, strings.Repeat("x", 5000)} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("POST", "/api/inspect/fix", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		(&app{}).inspectFIX(w, r)
		if w.Code < 400 {
			t.Fatalf("accepted invalid input: %d", w.Code)
		}
	}
}
