package main

import (
	"io"
)

// encodeReceiptFrame serialises a ReceiptFrame with a varint length prefix.
func encodeReceiptFrame(frame *ReceiptFrame) []byte {
	body := frame.marshal()
	return encodeLengthDelimited(body)
}

// decodeReceiptFrame reads one length-delimited ReceiptFrame from r.
func decodeReceiptFrame(r io.Reader) (*ReceiptFrame, error) {
	body, err := decodeLengthDelimited(r)
	if err != nil {
		return nil, err
	}
	frame := &ReceiptFrame{}
	if err := frame.unmarshal(body); err != nil {
		return nil, err
	}
	return frame, nil
}

// encodeControlFrame serialises a ControlFrame with a varint length prefix.
func encodeControlFrame(frame *ControlFrame) []byte {
	body := frame.marshal()
	return encodeLengthDelimited(body)
}

// decodeControlFrame reads one length-delimited ControlFrame from r.
func decodeControlFrame(r io.Reader) (*ControlFrame, error) {
	body, err := decodeLengthDelimited(r)
	if err != nil {
		return nil, err
	}
	frame := &ControlFrame{}
	if err := frame.unmarshal(body); err != nil {
		return nil, err
	}
	return frame, nil
}
