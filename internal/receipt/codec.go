// Package receipt provides the length-delimited protobuf codec for
// ReceiptFrame and ControlFrame on the AeroSync QUIC wire.
package receipt

import (
	"boltgo/internal/proto"
	"io"
)

// EncodeReceiptFrame serialises a ReceiptFrame with a varint length prefix.
func EncodeReceiptFrame(frame *proto.ReceiptFrame) []byte {
	body := frame.Marshal()
	return proto.EncodeLengthDelimited(body)
}

// DecodeReceiptFrame reads one length-delimited ReceiptFrame from r.
func DecodeReceiptFrame(r io.Reader) (*proto.ReceiptFrame, error) {
	body, err := proto.DecodeLengthDelimited(r)
	if err != nil {
		return nil, err
	}
	frame := &proto.ReceiptFrame{}
	if err := frame.Unmarshal(body); err != nil {
		return nil, err
	}
	return frame, nil
}

// EncodeControlFrame serialises a ControlFrame with a varint length prefix.
func EncodeControlFrame(frame *proto.ControlFrame) []byte {
	body := frame.Marshal()
	return proto.EncodeLengthDelimited(body)
}

// DecodeControlFrame reads one length-delimited ControlFrame from r.
func DecodeControlFrame(r io.Reader) (*proto.ControlFrame, error) {
	body, err := proto.DecodeLengthDelimited(r)
	if err != nil {
		return nil, err
	}
	frame := &proto.ControlFrame{}
	if err := frame.Unmarshal(body); err != nil {
		return nil, err
	}
	return frame, nil
}
