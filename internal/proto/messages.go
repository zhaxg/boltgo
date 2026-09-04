// Package proto implements wire-compatible protobuf types for the AeroSync
// wire protocol (aerosync/1). The encoding is hand-rolled to avoid requiring
// protoc while remaining byte-compatible with the Rust prost output.
package proto

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// ── ErrorCode enum ──

type ErrorCode int32

const (
	ErrUnspecified       ErrorCode = 0
	ErrAuth              ErrorCode = 1
	ErrChecksum          ErrorCode = 2
	ErrDiskFull          ErrorCode = 3
	ErrPermission        ErrorCode = 4
	ErrTimeout           ErrorCode = 5
	ErrCancelledRemote   ErrorCode = 6
	ErrInternal          ErrorCode = 99
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
	ID               string
	FromNode         string
	ToNode           string
	ContentType      string
	SizeBytes        uint64
	Sha256           string
	FileName         string
	Protocol         string
	SessionID        string
	TraceID          *string
	ConversationID   *string
	ParentFileIDs    []string
	CorrelationID    *string
	UserMetadata     map[string]string
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

// ControlFrame wraps one of: Handshake, TransferStart, Cancel.
type ControlFrame struct {
	Handshake      *Handshake
	TransferStart  *TransferStart
	Cancel         *Cancel
}

// BytesReceived is a progress frame.
type BytesReceived struct {
	Bytes uint64
}

// Received indicates checksum verification succeeded.
type Received struct {
	ChecksumOK bool
	Sha256     string
}

// Failed carries a structured error.
type Failed struct {
	Code   ErrorCode
	Detail string
}

// Acked is an application-level acknowledgement.
type Acked struct{}

// Nacked is an application-level rejection.
type Nacked struct {
	Reason string
}

// ReceiptFrame wraps one of: BytesReceived, Received, Failed, Acked, Nacked.
type ReceiptFrame struct {
	ReceiptID     string
	BytesReceived *BytesReceived
	Received      *Received
	Failed        *Failed
	Acked         *Acked
	Nacked        *Nacked
}

// ── Length-delimited framing helpers ──

// EncodeLengthDelimited encodes a protobuf message with a varint length prefix.
func EncodeLengthDelimited(body []byte) []byte {
	buf := make([]byte, 0, MaxVarintSize64+len(body))
	buf = AppendVarint(buf, uint64(len(body)))
	buf = append(buf, body...)
	return buf
}

// DecodeLengthDelimited reads a length-delimited protobuf message from r.
func DecodeLengthDelimited(r io.Reader) ([]byte, error) {
	length, err := ReadVarint(r)
	if err != nil {
		return nil, err
	}
	if length > 1024*1024 { // 1MB safety cap
		return nil, fmt.Errorf("frame too large: %d bytes", length)
	}
	body := make([]byte, length)
	if _, err := io.ReadFull(r, body); err != nil {
		return nil, err
	}
	return body, nil
}

// ── Protobuf varint encoding ──

const MaxVarintSize64 = 10

func AppendVarint(buf []byte, v uint64) []byte {
	for v >= 0x80 {
		buf = append(buf, byte(v)|0x80)
		v >>= 7
	}
	buf = append(buf, byte(v))
	return buf
}

func ReadVarint(r io.Reader) (uint64, error) {
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
	buf = AppendVarint(buf, v)
	return buf
}

func appendFieldBytes(buf []byte, fieldNumber int, data []byte) []byte {
	if len(data) == 0 {
		return buf
	}
	buf = append(buf, fieldTag(fieldNumber, wireLengthDelimited))
	buf = AppendVarint(buf, uint64(len(data)))
	buf = append(buf, data...)
	return buf
}

func appendString(buf []byte, fieldNumber int, s string) []byte {
	if s == "" {
		return buf
	}
	buf = append(buf, fieldTag(fieldNumber, wireLengthDelimited))
	buf = AppendVarint(buf, uint64(len(s)))
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
	data        []byte // for length-delimited
	value       uint64 // for varint
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
		case 5: // 32-bit
			if len(buf) < 4 {
				return nil, errors.New("truncated 32-bit field")
			}
			fields = append(fields, protoField{
				fieldNumber: fieldNumber,
				wireType:    wireType,
				value:       uint64(binary.LittleEndian.Uint32(buf[:4])),
			})
			buf = buf[4:]
		case 1: // 64-bit
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

// ── Marshal / Unmarshal for TransferStart ──

func (m *TransferStart) Marshal() []byte {
	var buf []byte
	buf = appendString(buf, 1, m.ReceiptID)
	buf = appendString(buf, 2, m.FileName)
	buf = appendFieldVarint(buf, 3, m.SizeBytes)
	buf = appendString(buf, 4, m.Sha256)
	if m.Metadata != nil {
		metaBytes := m.Metadata.Marshal()
		buf = appendFieldBytes(buf, 5, metaBytes)
	}
	buf = appendFieldVarint(buf, 6, uint64(m.ChunkSize))
	return buf
}

func (m *TransferStart) Unmarshal(buf []byte) error {
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
			if err := m.Metadata.Unmarshal(f.data); err != nil {
				return err
			}
		case 6:
			m.ChunkSize = uint32(f.value)
		}
	}
	return nil
}

// ── Marshal / Unmarshal for Metadata ──

func (m *Metadata) Marshal() []byte {
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
	// map<string, string> at field 99
	for k, v := range m.UserMetadata {
		entry := appendString(nil, 1, k)
		entry = appendString(entry, 2, v)
		buf = appendFieldBytes(buf, 99, entry)
	}
	return buf
}

func (m *Metadata) Unmarshal(buf []byte) error {
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
			// map entry
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

// ── Marshal / Unmarshal for ControlFrame ──

func (m *ControlFrame) Marshal() []byte {
	var buf []byte
	if m.Handshake != nil {
		buf = appendFieldBytes(buf, 1, m.Handshake.Marshal())
	}
	if m.TransferStart != nil {
		buf = appendFieldBytes(buf, 2, m.TransferStart.Marshal())
	}
	if m.Cancel != nil {
		buf = appendFieldBytes(buf, 3, m.Cancel.Marshal())
	}
	return buf
}

func (m *ControlFrame) Unmarshal(buf []byte) error {
	fields, err := decodeFields(buf)
	if err != nil {
		return err
	}
	for _, f := range fields {
		switch f.fieldNumber {
		case 1:
			m.Handshake = &Handshake{}
			if err := m.Handshake.Unmarshal(f.data); err != nil {
				return err
			}
		case 2:
			m.TransferStart = &TransferStart{}
			if err := m.TransferStart.Unmarshal(f.data); err != nil {
				return err
			}
		case 3:
			m.Cancel = &Cancel{}
			if err := m.Cancel.Unmarshal(f.data); err != nil {
				return err
			}
		}
	}
	return nil
}

// ── Marshal / Unmarshal for Cancel ──

func (m *Cancel) Marshal() []byte {
	var buf []byte
	buf = appendString(buf, 1, m.ReceiptID)
	buf = appendString(buf, 2, m.Reason)
	return buf
}

func (m *Cancel) Unmarshal(buf []byte) error {
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

// ── Marshal / Unmarshal for Handshake ──

func (m *Handshake) Marshal() []byte {
	var buf []byte
	buf = appendString(buf, 1, m.ProtocolVersion)
	buf = appendString(buf, 2, m.SenderNodeID)
	buf = appendFieldBytes(buf, 3, m.AuthProof)
	buf = appendFieldVarint(buf, 4, uint64(m.Capabilities))
	return buf
}

func (m *Handshake) Unmarshal(buf []byte) error {
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

// ── Marshal / Unmarshal for ReceiptFrame ──

func (m *ReceiptFrame) Marshal() []byte {
	var buf []byte
	buf = appendString(buf, 1, m.ReceiptID)
	if m.BytesReceived != nil {
		buf = appendFieldBytes(buf, 2, m.BytesReceived.Marshal())
	}
	if m.Received != nil {
		buf = appendFieldBytes(buf, 3, m.Received.Marshal())
	}
	if m.Failed != nil {
		buf = appendFieldBytes(buf, 4, m.Failed.Marshal())
	}
	if m.Acked != nil {
		buf = appendFieldBytes(buf, 5, m.Acked.Marshal())
	}
	if m.Nacked != nil {
		buf = appendFieldBytes(buf, 6, m.Nacked.Marshal())
	}
	return buf
}

func (m *ReceiptFrame) Unmarshal(buf []byte) error {
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
			if err := m.BytesReceived.Unmarshal(f.data); err != nil {
				return err
			}
		case 3:
			m.Received = &Received{}
			if err := m.Received.Unmarshal(f.data); err != nil {
				return err
			}
		case 4:
			m.Failed = &Failed{}
			if err := m.Failed.Unmarshal(f.data); err != nil {
				return err
			}
		case 5:
			m.Acked = &Acked{}
		case 6:
			m.Nacked = &Nacked{}
			if err := m.Nacked.Unmarshal(f.data); err != nil {
				return err
			}
		}
	}
	return nil
}

// ── Marshal / Unmarshal for sub-messages ──

func (m *BytesReceived) Marshal() []byte {
	return appendFieldVarint(nil, 1, m.Bytes)
}

func (m *BytesReceived) Unmarshal(buf []byte) error {
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

func (m *Received) Marshal() []byte {
	var buf []byte
	if m.ChecksumOK {
		buf = appendFieldVarint(buf, 1, 1)
	}
	buf = appendString(buf, 2, m.Sha256)
	return buf
}

func (m *Received) Unmarshal(buf []byte) error {
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

func (m *Failed) Marshal() []byte {
	var buf []byte
	buf = appendFieldVarint(buf, 1, uint64(m.Code))
	buf = appendString(buf, 2, m.Detail)
	return buf
}

func (m *Failed) Unmarshal(buf []byte) error {
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

func (m *Acked) Marshal() []byte {
	return nil
}

func (m *Acked) Unmarshal(buf []byte) error {
	return nil
}

func (m *Nacked) Marshal() []byte {
	return appendString(nil, 1, m.Reason)
}

func (m *Nacked) Unmarshal(buf []byte) error {
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
