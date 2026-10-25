// SPDX-License-Identifier: MIT

// fields.go — FIX 4.4 tag constants
//
// Only tags used by the bridge are defined here. Tags are grouped by
// their function: header, session-level, order entry, execution,
// and market data.
//
// Reference: FIX Protocol 4.4 specification
// https://www.fixtrading.org/standards/fix-4-4/

package codec

var lengthDataTags = map[int]int{
	90:  91,
	93:  89,
	95:  96,
	212: 213,
	348: 349,
	350: 351,
	352: 353,
	354: 355,
	356: 357,
	358: 359,
	360: 361,
	362: 363,
	364: 365,
	445: 446,
	618: 619,
	621: 622,
}

// ── Header tags (present in every message) ───────────────────────────────────

const (
	TagBeginString  = 8  // "FIX.4.4"
	TagBodyLength   = 9  // byte count of body (between tag 9 and tag 10)
	TagMsgType      = 35 // message type identifier
	TagSenderCompID = 49 // sender's firm identifier
	TagTargetCompID = 56 // target's firm identifier
	TagMsgSeqNum    = 34 // message sequence number (monotonically increasing)
	TagSendingTime  = 52 // UTC timestamp: YYYYMMDD-HH:MM:SS.sss
	TagCheckSum     = 10 // 3-digit checksum (sum of all bytes mod 256)
)

// ── Session-level message types (tag 35 values) ───────────────────────────────

const (
	MsgTypeLogon                 = "A"
	MsgTypeLogout                = "5"
	MsgTypeHeartbeat             = "0"
	MsgTypeTestRequest           = "1"
	MsgTypeResendRequest         = "2"
	MsgTypeReject                = "3"
	MsgTypeSequenceReset         = "4"
	MsgTypeBusinessMessageReject = "j"
)

// ── Application message types (tag 35 values) ────────────────────────────────

const (
	MsgTypeNewOrderSingle            = "D"
	MsgTypeOrderCancelRequest        = "F"
	MsgTypeOrderCancelReplaceRequest = "G"
	MsgTypeOrderCancelReject         = "9"
	MsgTypeExecutionReport           = "8"
	MsgTypeMarketDataRequest         = "V"
	MsgTypeMarketDataSnapshot        = "W"
	MsgTypeMarketDataIncremental     = "X"
	MsgTypeOrderStatusRequest        = "H" // not supported: answered with BusinessMessageReject
	MsgTypeOrderMassCancelRequest    = "q"
	MsgTypeOrderMassCancelReport     = "r"
)

// ── Session-level tags ────────────────────────────────────────────────────────

const (
	TagHeartBtInt           = 108 // heartbeat interval in seconds (in Logon)
	TagTestReqID            = 112 // test request ID (echoed in Heartbeat response)
	TagResetSeqNumFlag      = 141 // "Y" to reset sequence numbers on Logon
	TagEncryptMethod        = 98  // encryption method (0 = none)
	TagText                 = 58  // free-text description (used in Logout, Reject)
	TagRefSeqNum            = 45  // reference sequence number (in Reject)
	TagRefTagID             = 371 // tag number that caused a Reject
	TagSessionRejectReason  = 373 // coded reject reason
	TagBeginSeqNo           = 7   // first seqnum requested (ResendRequest)
	TagEndSeqNo             = 16  // last seqnum requested, 0 = infinity (ResendRequest)
	TagPossDupFlag          = 43  // "Y" on retransmitted messages
	TagOrigSendingTime      = 122 // original SendingTime of a retransmitted message
	TagGapFillFlag          = 123 // "Y" = SequenceReset in GapFill mode
	TagNewSeqNo             = 36  // next seqnum (SequenceReset)
	TagPassword             = 554 // Logon password
	TagRefMsgType           = 372 // MsgType being rejected (BusinessMessageReject)
	TagBusinessRejectReason = 380 // coded reason (BusinessMessageReject)
)

// ── BusinessRejectReason values (tag 380) ────────────────────────────────────

const (
	BusinessRejectReasonOther              = "0"
	BusinessRejectReasonUnsupportedMsgType = "3"
)

// ── SessionRejectReason values (tag 373) ──────────────────────────────────────

const (
	RejectRequiredTagMissing  = "1"
	RejectValueIncorrect      = "5"
	RejectIncorrectDataFormat = "6"
	RejectCompIDProblem       = "9"
	RejectSendingTimeAccuracy = "10"
	RejectInvalidMsgType      = "11"
)

// ── Order entry tags ──────────────────────────────────────────────────────────

const (
	TagClOrdID      = 11  // client order ID (unique per order)
	TagOrigClOrdID  = 41  // original client order ID (for cancel/replace)
	TagSymbol       = 55  // instrument symbol (e.g. "AAPL")
	TagSide         = 54  // 1=Buy, 2=Sell, 5=SellShort
	TagOrderQty     = 38  // order quantity
	TagOrdType      = 40  // 1=Market, 2=Limit, 3=Stop, 4=StopLimit
	TagPrice        = 44  // limit price (for Limit and StopLimit orders)
	TagStopPx       = 99  // stop price (for Stop and StopLimit orders)
	TagTimeInForce  = 59  // 0=Day, 1=GTC, 3=IOC, 4=FOK, 6=GTD, 7=AtClose
	TagTransactTime = 60  // transaction timestamp
	TagExpireDate   = 432 // expiry date for GTD orders (YYYYMMDD)
	TagAccount      = 1   // account identifier
	TagHandlInst    = 21  // handling instructions (1=automated, 2=manual)
	TagSecurityType = 167 // security type (CS=Common Stock, FUT=Future, etc.)
)

// ── Execution report tags ─────────────────────────────────────────────────────

const (
	TagOrderID          = 37  // exchange-assigned order ID
	TagExecID           = 17  // execution ID (unique per fill)
	TagExecType         = 150 // execution type: 0=New, F=Trade, 4=Cancel, 5=Replace, 8=Reject
	TagOrdStatus        = 39  // order status: 0=New, 1=PartFill, 2=Fill, 4=Cancel, 8=Reject
	TagLeavesQty        = 151 // remaining open quantity
	TagCumQty           = 14  // cumulative filled quantity
	TagAvgPx            = 6   // average fill price
	TagLastPx           = 31  // price of last fill
	TagLastQty          = 32  // quantity of last fill
	TagOrdRejReason     = 103 // order reject reason code
	TagCxlRejReason     = 102 // cancel reject reason code
	TagCxlRejResponseTo = 434 // message type being rejected (1=OrderCancelRequest, 2=OrderCancelReplaceRequest)

	// Self-match prevention (FIX 5.0 SP2, confirmed in the QuickFIX FIX50SP2 dictionary).
	TagSelfMatchPreventionID          = 2362 // STP group; empty = the order owner
	TagSelfMatchPreventionInstruction = 2964 // 1=cancel aggressive, 2=cancel passive, 3=both; 99=FXP decrement-and-cancel

	// Mass cancel (35=q / 35=r).
	TagMassCancelRequestType  = 530 // 1=by symbol, 7=all orders (others unsupported)
	TagMassCancelResponse     = 531 // 0=rejected, otherwise echoes 530
	TagMassCancelRejectReason = 532 // 0=not supported, 1=unknown security, 99=other
	TagTotalAffectedOrders    = 533

	// TagCancelOnDisconnect is an FXP user-defined Logon tag: Y cancels all of
	// the session's live orders (including parked stops) when it disconnects.
	TagCancelOnDisconnect = 8013
)

// ── Market data tags ──────────────────────────────────────────────────────────

const (
	TagMDReqID                 = 262 // market data request ID
	TagSubscriptionRequestType = 263 // 0=Snapshot, 1=Subscribe, 2=Unsubscribe
	TagMarketDepth             = 264 // 0=full book, N=top N levels
	TagMDUpdateType            = 265 // 0=Full Refresh, 1=Incremental
	TagNoMDEntryTypes          = 267 // number of entry type repeating groups
	TagMDEntryType             = 269 // 0=Bid, 1=Offer, 2=Trade
	TagNoRelatedSym            = 146 // number of symbols in MarketDataRequest
	TagNoMDEntries             = 268 // number of entries in market data message
	TagMDUpdateAction          = 279 // 0=New, 1=Change, 2=Delete
	TagMDEntryPx               = 270 // price of market data entry
	TagMDEntrySize             = 271 // size of market data entry
	TagMDEntryTime             = 273 // entry time: HH:MM:SS.sss
)

// ── Side values (tag 54) ──────────────────────────────────────────────────────

const (
	SideBuy       = "1"
	SideSell      = "2"
	SideSellShort = "5"
)

// ── OrdType values (tag 40) ───────────────────────────────────────────────────

const (
	OrdTypeMarket    = "1"
	OrdTypeLimit     = "2"
	OrdTypeStop      = "3"
	OrdTypeStopLimit = "4"
)

// ── TimeInForce values (tag 59) ───────────────────────────────────────────────

const (
	TIFDay     = "0"
	TIFGTC     = "1"
	TIFIOC     = "3"
	TIFFOK     = "4"
	TIFGTD     = "6"
	TIFAtClose = "7"
	TIFAtOpen  = "A" // non-standard extension, used by some venues
)

// ── ExecType values (tag 150) ─────────────────────────────────────────────────

const (
	ExecTypeNew         = "0"
	ExecTypePartialFill = "1"
	ExecTypeFill        = "2"
	ExecTypeCanceled    = "4"
	ExecTypeReplaced    = "5"
	ExecTypeRejected    = "8"
	ExecTypeExpired     = "C"
	ExecTypeTrade       = "F" // FIX 4.4: any fill; partial vs full is in OrdStatus
	ExecTypeRestated    = "D"
)

// ── OrdStatus values (tag 39) ─────────────────────────────────────────────────

const (
	OrdStatusNew             = "0"
	OrdStatusPartiallyFilled = "1"
	OrdStatusFilled          = "2"
	OrdStatusCanceled        = "4"
	OrdStatusReplaced        = "5"
	OrdStatusRejected        = "8"
	OrdStatusExpired         = "C"
)

// ── OrdRejReason values (tag 103) ────────────────────────────────────────────

const (
	OrdRejReasonUnknownSymbol      = "1"
	OrdRejReasonExchangeClosed     = "2"
	OrdRejReasonOrderExceedsLimit  = "3"
	OrdRejReasonUnknownOrder       = "5"
	OrdRejReasonDuplicateOrder     = "6"
	OrdRejReasonUnsupportedOrdType = "13"
	OrdRejReasonOther              = "99"
)

// ── EncryptMethod values (tag 98) ────────────────────────────────────────────

const (
	EncryptMethodNone = "0"
)

// ── SessionRejectReason values (tag 373) ─────────────────────────────────────

const (
	SessionRejectReasonInvalidTagNumber        = "0"
	SessionRejectReasonRequiredTagMissing      = "1"
	SessionRejectReasonTagNotDefinedForMsgType = "2"
	SessionRejectReasonUndefinedTag            = "3"
	SessionRejectReasonTagWithoutValue         = "4"
	SessionRejectReasonValueIncorrect          = "5"
	SessionRejectReasonIncorrectDataFormat     = "6"
	SessionRejectReasonCompIDProblem           = "9"
	SessionRejectReasonOther                   = "99"
)
