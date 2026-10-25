// SPDX-License-Identifier: MIT
package codec

import "bytes"

// ReverseRoute preserves sender/target session identity and reverses only the
// third-party routing headers. Empty values are never emitted. The source
// message is explicit so asynchronous replies cannot inherit another request.
func ReverseRoute(frame []byte, request *Message) []byte {
	if request == nil {
		return frame
	}
	m, err := NewParser(bytes.NewReader(frame)).ReadMessage()
	if err != nil {
		return frame
	}
	b := NewBuilder(m.MsgType(), m.Get(TagSenderCompID), m.Get(TagTargetCompID), m.GetInt(TagMsgSeqNum))
	b.Set(TagSendingTime, m.Get(TagSendingTime))
	for _, pair := range [][2]int{{115, 128}, {116, 129}, {144, 145}, {128, 115}, {129, 116}, {145, 144}, {50, 57}, {57, 50}, {142, 143}, {143, 142}} {
		if value := request.Get(pair[0]); value != "" {
			b.Set(pair[1], value)
		}
	}
	for _, f := range m.Tags {
		switch f.Tag {
		case 8, 9, 10, 35, 49, 56, 34, 52, 115, 116, 144, 128, 129, 145, 50, 57, 142, 143:
			continue
		}
		b.Append(f.Tag, f.Value)
	}
	return b.Build()
}
