// SPDX-License-Identifier: MIT
package main

import (
	"encoding/base64"
	"net/http"
	"strings"

	"fix_bridge/translator"
	"go_gateway/protobuf"
	"go_gateway/symbolconfig"
	"google.golang.org/protobuf/proto"
)

// Accept the exact binary WS envelope, not an independently reconstructed order.
func (a *app) inspectFIX(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Frame string `json:"frame"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	raw, err := base64.StdEncoding.DecodeString(req.Frame)
	if err != nil || len(raw) == 0 || len(raw) > 2048 {
		http.Error(w, "invalid new-order frame", http.StatusBadRequest)
		return
	}
	msg := new(protobuf.FXPMessage)
	if proto.Unmarshal(raw, msg) != nil || msg.GetNewOrder() == nil || len(msg.ProtoReflect().GetUnknown()) > 0 || len(msg.GetNewOrder().ProtoReflect().GetUnknown()) > 0 {
		http.Error(w, "expected known new-order frame", http.StatusBadRequest)
		return
	}
	rules := symbolconfig.Config{}
	for _, s := range a.symbols {
		rules[s.Symbol] = symbolconfig.Rules{PriceScale: s.PriceScale}
	}
	fix, err := translator.RenderNewOrderFIX(msg.GetNewOrder(), rules)
	if err != nil {
		http.Error(w, "order cannot be represented as FIX 4.4", http.StatusUnprocessableEntity)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"fix": strings.ReplaceAll(string(fix), "\x01", "|"), "fix_bytes": len(fix), "fxp_bytes": len(raw), "note": "Illustrative FIX client header: CONSOLE → FXPBRIDGE, sequence 1. Sizes include each application envelope; exclude WS/TCP/TLS framing. No protocol speed ranking."})
}
