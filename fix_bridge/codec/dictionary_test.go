// SPDX-License-Identifier: MIT

package codec

import "testing"

func TestMissingRequired(t *testing.T) {
	nos := map[int]string{TagMsgType: MsgTypeNewOrderSingle, TagClOrdID: "O1", TagSymbol: "MSFT",
		TagSide: SideBuy, TagOrdType: "2", TagTransactTime: "20260101-00:00:00"}
	mdr := map[int]string{TagMsgType: MsgTypeMarketDataRequest, TagMDReqID: "R1",
		TagSubscriptionRequestType: "1", TagMarketDepth: "0", TagNoMDEntryTypes: "1",
		TagMDEntryType: "0", TagNoRelatedSym: "1", TagSymbol: "MSFT"}
	without := func(f map[int]string, tag int) *Message {
		c := map[int]string{}
		for k, v := range f {
			if k != tag {
				c[k] = v
			}
		}
		return &Message{Fields: c}
	}
	cases := []struct {
		name string
		msg  *Message
		want int
	}{
		{"complete NOS", without(nos, 0), 0},
		{"NOS without TransactTime", without(nos, TagTransactTime), TagTransactTime},
		{"NOS without Side", without(nos, TagSide), TagSide},
		{"NOS without Symbol", without(nos, TagSymbol), TagSymbol},
		{"complete MarketDataRequest", without(mdr, 0), 0},
		{"MarketDataRequest without MarketDepth", without(mdr, TagMarketDepth), TagMarketDepth},
		{"MarketDataRequest without MDEntryType", without(mdr, TagMDEntryType), TagMDEntryType},
		{"unlisted MsgType", &Message{Fields: map[int]string{TagMsgType: "H"}}, 0},
	}
	for _, c := range cases {
		f, missing := MissingRequired(c.msg)
		if c.want == 0 && missing {
			t.Errorf("%s: unexpected missing %d (%s)", c.name, f.Tag, f.Name)
		}
		if c.want != 0 && (!missing || f.Tag != c.want) {
			t.Errorf("%s: got (%d, %v), want %d", c.name, f.Tag, missing, c.want)
		}
	}
	for msgType, fields := range RequiredFields {
		for _, f := range fields {
			if f.Name == "" || f.Tag <= 0 {
				t.Errorf("%s: bad entry %+v", msgType, f)
			}
		}
	}
}
