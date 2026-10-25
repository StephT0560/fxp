// SPDX-License-Identifier: MIT
package transport

import (
	"encoding/binary"
	"go_gateway/protobuf"
	"google.golang.org/protobuf/proto"
)

// marshalOrderFrame is the existing two-buffer path, isolated for A/B profiling.
func marshalOrderFrame(msg *protobuf.FXPMessage) ([]byte, error) {
	encoded, err := proto.Marshal(msg)
	if err != nil {
		return nil, err
	}
	frame := make([]byte, 4+len(encoded))
	binary.BigEndian.PutUint32(frame[:4], uint32(len(encoded)))
	copy(frame[4:], encoded)
	return frame, nil
}
