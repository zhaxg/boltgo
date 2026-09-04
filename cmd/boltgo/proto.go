// Package main implements wire-compatible protobuf types for the AeroSync
// wire protocol (aerosync/1). Hand-rolled to avoid protoc while remaining
// byte-compatible with the Rust prost output.
package main

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// ── ErrorCode enum ──

type ErrorCode int32

const (
	ErrUnspecified     ErrorCode = 0
	ErrAuth            ErrorCode = 1
	ErrChecksum        ErrorCode = 2
	ErrDiskFull        ErrorCode = 3
	ErrPermission      ErrorCode = 4
	ErrTimeout         ErrorCode = 5
	ErrCancelledRemote ErrorCode = 6
	ErrInternal        ErrorCode = 99
)

// ── Message types ──

type TransferStart struct {
	ReceiptID string
	FileName  string
	SizeBytes uint64
	Sha256    string
	Metadata  *Metadata
	ChunkSize uint32
}

type Metadata struct {
	ID             string
	FromNode       string
	ToNode         string
	ContentType    string
	SizeBytes      uint64
	Sha256         string
	FileName       string
	Protocol       string
	SessionID      string
	TraceID        *string
	ConversationID *string
	ParentFileIDs  []string
	CorrelationID  *string
	UserMetadata   map[string]string
}

type Handshake struct {
	ProtocolVersion string
	SenderNodeID    string
	AuthProof       []byte
	Capabilities    uint32
}

type Cancel struct {
	ReceiptID string
	Reason    string
}

type ControlFrame struct {
	Handshake     *Handshake
	TransferStart *TransferStart
	Cancel        *Cancel
}

type BytesReceived struct {
	Bytes uint64
}

type Received struct {
	ChecksumOK bool
	Sha256     string
}

type Failed struct {
	Code   ErrorCode
	Detail string
}

type Acked struct{}

type Nacked struct {
	Reason string
}

type ReceiptFrame struct {
	ReceiptID     string
	BytesReceived *BytesReceived
	Received      *Received
	Failed        *Failed
	Acked         *Acked
	Nacked        *Nacked
}

// ── Length-delimited framing ──

func encodeLengthDelimited(body []byte) []byte {
	buf := make([]byte, 0, maxVarintSize64+len(body))
	buf = appendVarint(buf, uint64(len(body)))
	buf = append(buf, body...)
	return buf
}

func decodeLengthDelimited(r io.Reader) ([]byte, error) {
	length, err := readVarint(r)
	if err != nil {
		return nil, err
	}
	if length > 1024*1024 {
		return nil, fmt.Errorf("frame too large: %d bytes", length)
	}
	body := make([]byte, length)
	if _, err := io.ReadFull(r, body); err != nil {
		return nil, err
	}
	return body, nil
}

// ── Protobuf varint encoding ──

const maxVarintSize64 = 10

func appendVarint(buf []byte, v uint64) []byte {
	for v >= 0x80 {
		buf = append(buf, byte(v)|0x80)
		v >>= 7
	}
	buf = append(buf, byte(v))
	return buf
}

func readVarint(r io.Reader) (uint64, error) {
	var result uint64
	var shift uint
	for {
		var b [1]byte
		if _, err := io.ReadFull(r, b[:]); err != nil {
			return 0, err
		}
		result |= uint64(b[0]&0x7f) << shift
		if b[0]&0x80 == 0 {
			return result, nil
		}
		shift += 7
		if shift >= 64 {
			return 0, errors.New("varint overflows uint64")
		}
	}
}

// ── Field encoding helpers ──

func fieldTag(fieldNumber int, wireType int) byte {
	return byte((fieldNumber << 3) | wireType)
}

const (
	wireVarint          = 0
	wireLengthDelimited = 2
)

func appendFieldVarint(buf []byte, fieldNumber int, v uint64) []byte {
	buf = append(buf, fieldTag(fieldNumber, wireVarint))
	buf = appendVarint(buf, v)
	return buf
}

func appendFieldBytes(buf []byte, fieldNumber int, data []byte) []byte {
	if len(data) == 0 {
		return buf
	}
	buf = append(buf, fieldTag(fieldNumber, wireLengthDelimited))
	buf = appendVarint(buf, uint64(len(data)))
	buf = append(buf, data...)
	return buf
}

func appendString(buf []byte, fieldNumber int, s string) []byte {
	if s == "" {
		return buf
	}
	buf = append(buf, fieldTag(fieldNumber, wireLengthDelimited))
	buf = appendVarint(buf, uint64(len(s)))
	buf = append(buf, s...)
	return buf
}

func appendStringOptional(buf []byte, fieldNumber int, s *string) []byte {
	if s == nil {
		return buf
	}
	return appendString(buf, fieldNumber, *s)
}

// ── Protobuf field decoder ──

type protoField struct {
	fieldNumber int
	wireType    int
	data        []byte
	value       uint64
}

func decodeFields(buf []byte) ([]protoField, error) {
	var fields []protoField
	for len(buf) > 0 {
		if len(buf) < 2 {
			return nil, errors.New("truncated field")
		}
		tag := buf[0]
		fieldNumber := int(tag >> 3)
		wireType := int(tag & 0x07)
		buf = buf[1:]

		switch wireType {
		case wireVarint:
			v, n, err := decodeVarint(buf)
			if err != nil {
				return nil, err
			}
			fields = append(fields, protoField{fieldNumber: fieldNumber, wireType: wireType, value: v})
			buf = buf[n:]
		case wireLengthDelimited:
			length, n, err := decodeVarint(buf)
			if err != nil {
				return nil, err
			}
			buf = buf[n:]
			if uint64(len(buf)) < length {
				return nil, errors.New("truncated length-delimited field")
			}
			fields = append(fields, protoField{
				fieldNumber: fieldNumber,
				wireType:    wireType,
				data:        buf[:length],
			})
			buf = buf[length:]
		case 5:
			if len(buf) < 4 {
				return nil, errors.New("truncated 32-bit field")
			}
			fields = append(fields, protoField{
				fieldNumber: fieldNumber,
				wireType:    wireType,
				value:       uint64(binary.LittleEndian.Uint32(buf[:4])),
			})
			buf = buf[4:]
		case 1:
			if len(buf) < 8 {
				return nil, errors.New("truncated 64-bit field")
			}
			fields = append(fields, protoField{
				fieldNumber: fieldNumber,
				wireType:    wireType,
				value:       binary.LittleEndian.Uint64(buf[:8]),
			})
			buf = buf[8:]
		default:
			return nil, fmt.Errorf("unknown wire type %d", wireType)
		}
	}
	return fields, nil
}

func decodeVarint(buf []byte) (uint64, int, error) {
	var result uint64
	var shift uint
	for i, b := range buf {
		result |= uint64(b&0x7f) << shift
		if b&0x80 == 0 {
			return result, i + 1, nil
		}
		shift += 7
		if shift >= 64 {
			return 0, 0, errors.New("varint overflows uint64")
		}
	}
	return 0, 0, errors.New("truncated varint")
}

// ── Marshal / Unmarshal ──

func (m *TransferStart) marshal() []byte {
	var buf []byte
	buf = appendString(buf, 1, m.ReceiptID)
	buf = appendString(buf, 2, m.FileName)
	buf = appendFieldVarint(buf, 3, m.SizeBytes)
	buf = appendString(buf, 4, m.Sha256)
	if m.Metadata != nil {
		buf = appendFieldBytes(buf, 5, m.Metadata.marshal())
	}
	buf = appendFieldVarint(buf, 6, uint64(m.ChunkSize))
	return buf
}

func (m *TransferStart) unmarshal(buf []byte) error {
	fields, err := decodeFields(buf)
	if err != nil {
		return err
	}
	for _, f := range fields {
		switch f.fieldNumber {
		case 1:
			m.ReceiptID = string(f.data)
		case 2:
			m.FileName = string(f.data)
		case 3:
			m.SizeBytes = f.value
		case 4:
			m.Sha256 = string(f.data)
		case 5:
			m.Metadata = &Metadata{}
			if err := m.Metadata.unmarshal(f.data); err != nil {
				return err
			}
		case 6:
			m.ChunkSize = uint32(f.value)
		}
	}
	return nil
}

func (m *Metadata) marshal() []byte {
	var buf []byte
	buf = appendString(buf, 1, m.ID)
	buf = appendString(buf, 2, m.FromNode)
	buf = appendString(buf, 3, m.ToNode)
	buf = appendString(buf, 5, m.ContentType)
	buf = appendFieldVarint(buf, 6, m.SizeBytes)
	buf = appendString(buf, 7, m.Sha256)
	buf = appendString(buf, 8, m.FileName)
	buf = appendString(buf, 9, m.Protocol)
	buf = appendString(buf, 10, m.SessionID)
	buf = appendStringOptional(buf, 20, m.TraceID)
	buf = appendStringOptional(buf, 21, m.ConversationID)
	for _, id := range m.ParentFileIDs {
		buf = appendString(buf, 22, id)
	}
	buf = appendStringOptional(buf, 25, m.CorrelationID)
	for k, v := range m.UserMetadata {
		entry := appendString(nil, 1, k)
		entry = appendString(entry, 2, v)
		buf = appendFieldBytes(buf, 99, entry)
	}
	return buf
}

func (m *Metadata) unmarshal(buf []byte) error {
	fields, err := decodeFields(buf)
	if err != nil {
		return err
	}
	for _, f := range fields {
		switch f.fieldNumber {
		case 1:
			m.ID = string(f.data)
		case 2:
			m.FromNode = string(f.data)
		case 3:
			m.ToNode = string(f.data)
		case 5:
			m.ContentType = string(f.data)
		case 6:
			m.SizeBytes = f.value
		case 7:
			m.Sha256 = string(f.data)
		case 8:
			m.FileName = string(f.data)
		case 9:
			m.Protocol = string(f.data)
		case 10:
			m.SessionID = string(f.data)
		case 20:
			s := string(f.data)
			m.TraceID = &s
		case 21:
			s := string(f.data)
			m.ConversationID = &s
		case 22:
			m.ParentFileIDs = append(m.ParentFileIDs, string(f.data))
		case 25:
			s := string(f.data)
			m.CorrelationID = &s
		case 99:
			if m.UserMetadata == nil {
				m.UserMetadata = make(map[string]string)
			}
			var key, val string
			entryFields, err := decodeFields(f.data)
			if err != nil {
				return err
			}
			for _, ef := range entryFields {
				switch ef.fieldNumber {
				case 1:
					key = string(ef.data)
				case 2:
					val = string(ef.data)
				}
			}
			m.UserMetadata[key] = val
		}
	}
	return nil
}

func (m *ControlFrame) marshal() []byte {
	var buf []byte
	if m.Handshake != nil {
		buf = appendFieldBytes(buf, 1, m.Handshake.marshal())
	}
	if m.TransferStart != nil {
		buf = appendFieldBytes(buf, 2, m.TransferStart.marshal())
	}
	if m.Cancel != nil {
		buf = appendFieldBytes(buf, 3, m.Cancel.marshal())
	}
	return buf
}

func (m *ControlFrame) unmarshal(buf []byte) error {
	fields, err := decodeFields(buf)
	if err != nil {
		return err
	}
	for _, f := range fields {
		switch f.fieldNumber {
		case 1:
			m.Handshake = &Handshake{}
			if err := m.Handshake.unmarshal(f.data); err != nil {
				return err
			}
		case 2:
			m.TransferStart = &TransferStart{}
			if err := m.TransferStart.unmarshal(f.data); err != nil {
				return err
			}
		case 3:
			m.Cancel = &Cancel{}
			if err := m.Cancel.unmarshal(f.data); err != nil {
				return err
			}
		}
	}
	return nil
}

func (m *Cancel) marshal() []byte {
	var buf []byte
	buf = appendString(buf, 1, m.ReceiptID)
	buf = appendString(buf, 2, m.Reason)
	return buf
}

func (m *Cancel) unmarshal(buf []byte) error {
	fields, err := decodeFields(buf)
	if err != nil {
		return err
	}
	for _, f := range fields {
		switch f.fieldNumber {
		case 1:
			m.ReceiptID = string(f.data)
		case 2:
			m.Reason = string(f.data)
		}
	}
	return nil
}

func (m *Handshake) marshal() []byte {
	var buf []byte
	buf = appendString(buf, 1, m.ProtocolVersion)
	buf = appendString(buf, 2, m.SenderNodeID)
	buf = appendFieldBytes(buf, 3, m.AuthProof)
	buf = appendFieldVarint(buf, 4, uint64(m.Capabilities))
	return buf
}

func (m *Handshake) unmarshal(buf []byte) error {
	fields, err := decodeFields(buf)
	if err != nil {
		return err
	}
	for _, f := range fields {
		switch f.fieldNumber {
		case 1:
			m.ProtocolVersion = string(f.data)
		case 2:
			m.SenderNodeID = string(f.data)
		case 3:
			m.AuthProof = f.data
		case 4:
			m.Capabilities = uint32(f.value)
		}
	}
	return nil
}

func (m *ReceiptFrame) marshal() []byte {
	var buf []byte
	buf = appendString(buf, 1, m.ReceiptID)
	if m.BytesReceived != nil {
		buf = appendFieldBytes(buf, 2, m.BytesReceived.marshal())
	}
	if m.Received != nil {
		buf = appendFieldBytes(buf, 3, m.Received.marshal())
	}
	if m.Failed != nil {
		buf = appendFieldBytes(buf, 4, m.Failed.marshal())
	}
	if m.Acked != nil {
		buf = appendFieldBytes(buf, 5, m.Acked.marshal())
	}
	if m.Nacked != nil {
		buf = appendFieldBytes(buf, 6, m.Nacked.marshal())
	}
	return buf
}

func (m *ReceiptFrame) unmarshal(buf []byte) error {
	fields, err := decodeFields(buf)
	if err != nil {
		return err
	}
	for _, f := range fields {
		switch f.fieldNumber {
		case 1:
			m.ReceiptID = string(f.data)
		case 2:
			m.BytesReceived = &BytesReceived{}
			if err := m.BytesReceived.unmarshal(f.data); err != nil {
				return err
			}
		case 3:
			m.Received = &Received{}
			if err := m.Received.unmarshal(f.data); err != nil {
				return err
			}
		case 4:
			m.Failed = &Failed{}
			if err := m.Failed.unmarshal(f.data); err != nil {
				return err
			}
		case 5:
			m.Acked = &Acked{}
		case 6:
			m.Nacked = &Nacked{}
			if err := m.Nacked.unmarshal(f.data); err != nil {
				return err
			}
		}
	}
	return nil
}

func (m *BytesReceived) marshal() []byte {
	return appendFieldVarint(nil, 1, m.Bytes)
}

func (m *BytesReceived) unmarshal(buf []byte) error {
	fields, err := decodeFields(buf)
	if err != nil {
		return err
	}
	for _, f := range fields {
		if f.fieldNumber == 1 {
			m.Bytes = f.value
		}
	}
	return nil
}

func (m *Received) marshal() []byte {
	var buf []byte
	if m.ChecksumOK {
		buf = appendFieldVarint(buf, 1, 1)
	}
	buf = appendString(buf, 2, m.Sha256)
	return buf
}

func (m *Received) unmarshal(buf []byte) error {
	fields, err := decodeFields(buf)
	if err != nil {
		return err
	}
	for _, f := range fields {
		switch f.fieldNumber {
		case 1:
			m.ChecksumOK = f.value != 0
		case 2:
			m.Sha256 = string(f.data)
		}
	}
	return nil
}

func (m *Failed) marshal() []byte {
	var buf []byte
	buf = appendFieldVarint(buf, 1, uint64(m.Code))
	buf = appendString(buf, 2, m.Detail)
	return buf
}

func (m *Failed) unmarshal(buf []byte) error {
	fields, err := decodeFields(buf)
	if err != nil {
		return err
	}
	for _, f := range fields {
		switch f.fieldNumber {
		case 1:
			m.Code = ErrorCode(f.value)
		case 2:
			m.Detail = string(f.data)
		}
	}
	return nil
}

func (m *Acked) marshal() []byte    { return nil }
func (m *Acked) unmarshal(_ []byte) error { return nil }

func (m *Nacked) marshal() []byte {
	return appendString(nil, 1, m.Reason)
}

func (m *Nacked) unmarshal(buf []byte) error {
	fields, err := decodeFields(buf)
	if err != nil {
		return err
	}
	for _, f := range fields {
		if f.fieldNumber == 1 {
			m.Reason = string(f.data)
		}
	}
	return nil
}
