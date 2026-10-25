// SPDX-License-Identifier: MIT
package transport

import (
	"encoding/binary"
	"go_gateway/protobuf"
	"google.golang.org/protobuf/proto"
)

// marshalOrderFrame is a single-buffer candidate for A/B profiling.
func marshalOrderFrame(msg *protobuf.FXPMessage) ([]byte, error) {
	frame, err := (proto.MarshalOptions{}).MarshalAppend(make([]byte, 4, 4+proto.Size(msg)), msg)
	if err != nil {
		return nil, err
	}
	binary.BigEndian.PutUint32(frame[:4], uint32(len(frame)-4))
	return frame, nil
}
