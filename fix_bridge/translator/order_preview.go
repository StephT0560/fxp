// SPDX-License-Identifier: MIT
package translator

import (
	"fmt"
	"strings"
	"time"

	"fix_bridge/codec"
	"go_gateway/protobuf"
	"go_gateway/symbolconfig"
)

// RenderNewOrderFIX renders the client-side FIX 4.4 equivalent of a new order.
// It does not transmit, allocate a session sequence, or claim byte equivalence
// for FXP-only routing/header fields. Callers supply venue rules explicitly.
func RenderNewOrderFIX(o *protobuf.NewOrderSingle, rules symbolconfig.Config) ([]byte, error) {
	if o == nil || o.ClientOrderId == "" || o.Symbol == "" || o.Quantity <= 0 || o.Timestamp == nil || o.Timestamp.CheckValid() != nil {
		return nil, fmt.Errorf("order requires id, symbol, quantity and timestamp")
	}
	for _, s := range []string{o.ClientOrderId, o.Symbol, o.Account, o.StpGroup, o.ExpireDate} {
		if len(s) > 128 || strings.ContainsAny(s, "\x01|\r\n\x00") {
			return nil, fmt.Errorf("invalid FIX field")
		}
	}
	side, err := mapSideToFIX(o.Side)
	if err != nil {
		return nil, err
	}
	// Invert the authoritative inbound mappings, rejecting unknown enum values.
	ordType := ""
	for _, v := range []string{"1", "2", "3", "4"} {
		if mapped, e := mapOrdType(v); e == nil && mapped == o.OrderType {
			ordType = v
		}
	}
	tif := ""
	for _, v := range []string{"0", "1", "2", "3", "4", "6", "7"} {
		if mapped, e := mapTimeInForce(v); e == nil && mapped == o.TimeInForce {
			tif = v
		}
	}
	if ordType == "" || tif == "" {
		return nil, fmt.Errorf("unsupported order type or time in force")
	}
	if o.DisplayQuantity < 0 || o.DisplayQuantity > o.Quantity {
		return nil, fmt.Errorf("invalid display quantity")
	}
	if (ordType == "2" || ordType == "4") && o.Price <= 0 || (ordType == "3" || ordType == "4") && o.StopPrice <= 0 {
		return nil, fmt.Errorf("required positive price missing")
	}
	stp := ""
	stpOK := false
	for _, v := range []string{"", "1", "2", "3", "99"} {
		if mapped, e := mapSTPInstruction(v); e == nil && mapped == o.StpMode {
			stp = v
			stpOK = true
		}
	}
	if !stpOK {
		return nil, fmt.Errorf("unsupported STP mode")
	}
	b := codec.NewBuilder("D", "CONSOLE", "FXPBRIDGE", 1)
	b.Set(52, formatProtoTime(o.Timestamp)).Set(11, o.ClientOrderId).Set(55, o.Symbol).Set(54, side).SetInt(38, int(o.Quantity)).Set(40, ordType).Set(59, tif).Set(60, formatProtoTime(o.Timestamp))
	if o.Price != 0 {
		b.Set(44, rules.FormatPrice(o.Symbol, o.Price))
	}
	if o.StopPrice != 0 {
		b.Set(99, rules.FormatPrice(o.Symbol, o.StopPrice))
	}
	if o.Account != "" {
		b.Set(1, o.Account)
	}
	if o.ExpireDate != "" {
		d, e := time.Parse("2006-01-02", o.ExpireDate)
		if e != nil {
			return nil, fmt.Errorf("invalid expiry")
		}
		b.Set(432, d.Format("20060102"))
	} else if tif == "6" {
		return nil, fmt.Errorf("GTD requires expiry")
	}
	if o.DisplayQuantity > 0 {
		b.SetInt(111, int(o.DisplayQuantity))
	}
	b.Set(codec.TagSelfMatchPreventionInstruction, stp)
	if o.StpGroup != "" {
		b.Set(codec.TagSelfMatchPreventionID, o.StpGroup)
	}
	return b.Build(), nil
}
