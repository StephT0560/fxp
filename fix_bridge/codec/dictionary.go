// SPDX-License-Identifier: MIT

package codec

// RequiredField is a tag with its FIX 4.4 field name.
type RequiredField struct {
	Tag  int
	Name string
}

// RequiredFields is a minimal FIX 4.4 data dictionary: for each supported
// inbound application MsgType, the body fields the spec marks required,
// including the required fields of required components (Instrument: Symbol)
// and repeating groups (NoMDEntryTypes: MDEntryType, NoRelatedSym: Symbol).
// OrderQtyData is a required component in D and G but none of its fields is
// individually required, so OrderQty(38) is validated by the translator.
var RequiredFields = map[string][]RequiredField{
	MsgTypeNewOrderSingle: {
		{TagClOrdID, "ClOrdID"}, {TagSymbol, "Symbol"}, {TagSide, "Side"},
		{TagTransactTime, "TransactTime"}, {TagOrdType, "OrdType"},
	},
	MsgTypeOrderCancelRequest: {
		{TagOrigClOrdID, "OrigClOrdID"}, {TagClOrdID, "ClOrdID"}, {TagSymbol, "Symbol"},
		{TagSide, "Side"}, {TagTransactTime, "TransactTime"},
	},
	MsgTypeOrderCancelReplaceRequest: {
		{TagOrigClOrdID, "OrigClOrdID"}, {TagClOrdID, "ClOrdID"}, {TagSymbol, "Symbol"},
		{TagSide, "Side"}, {TagTransactTime, "TransactTime"}, {TagOrdType, "OrdType"},
	},
	MsgTypeMarketDataRequest: {
		{TagMDReqID, "MDReqID"}, {TagSubscriptionRequestType, "SubscriptionRequestType"},
		{TagMarketDepth, "MarketDepth"}, {TagNoMDEntryTypes, "NoMDEntryTypes"},
		{TagMDEntryType, "MDEntryType"}, {TagNoRelatedSym, "NoRelatedSym"}, {TagSymbol, "Symbol"},
	},
}

// MissingRequired returns the first required field absent from m, if m's
// MsgType is in RequiredFields.
func MissingRequired(m *Message) (RequiredField, bool) {
	for _, f := range RequiredFields[m.MsgType()] {
		if m.Get(f.Tag) == "" {
			return f, true
		}
	}
	return RequiredField{}, false
}
