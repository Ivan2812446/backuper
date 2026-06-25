// Package protocol — бинарный протокол обмена Backuper (раздел 5 ТЗ):
// кадры, типы сообщений, коды ошибок, кодеки полезной нагрузки (big-endian).
package protocol

import (
	"encoding/binary"
	"fmt"
	"io"
)

// ProtoVersion — версия протокола, передаётся в каждом кадре (5.2) и в AUTH_REQ.
const ProtoVersion uint8 = 1

// HeaderSize — размер заголовка кадра: версия(1) + тип(1) + длина(8).
const HeaderSize = 10

// Типы сообщений (5.4).
const (
	MsgAuthReq   byte = 0x01
	MsgAuthResp  byte = 0x02
	MsgListReq   byte = 0x10
	MsgListBatch byte = 0x11
	MsgGetReq    byte = 0x20
	MsgGetResp   byte = 0x21
	MsgFileData  byte = 0x22
	MsgFileEnd   byte = 0x23
	MsgPutReq    byte = 0x30
	MsgPutResp   byte = 0x31
	MsgDiskReq   byte = 0x40
	MsgDiskResp  byte = 0x41
	MsgPing      byte = 0xF0
	MsgPong      byte = 0xF1
	MsgError     byte = 0xFF
)

// Коды ошибок (ERROR.code, 5.5).
const (
	ErrAuthFailed       uint16 = 1
	ErrNotFound         uint16 = 2
	ErrPermissionDenied uint16 = 3
	ErrIOError          uint16 = 4
	ErrDiskFull         uint16 = 5
	ErrBadOffset        uint16 = 6
	ErrBusy             uint16 = 7
	ErrProtocol         uint16 = 8
	ErrSizeMismatch     uint16 = 9
	ErrUnsupported      uint16 = 10
)

// MsgName — человекочитаемое имя типа сообщения (для логов).
func MsgName(t byte) string {
	switch t {
	case MsgAuthReq:
		return "AUTH_REQ"
	case MsgAuthResp:
		return "AUTH_RESP"
	case MsgListReq:
		return "LIST_REQ"
	case MsgListBatch:
		return "LIST_BATCH"
	case MsgGetReq:
		return "GET_REQ"
	case MsgGetResp:
		return "GET_RESP"
	case MsgFileData:
		return "FILE_DATA"
	case MsgFileEnd:
		return "FILE_END"
	case MsgPutReq:
		return "PUT_REQ"
	case MsgPutResp:
		return "PUT_RESP"
	case MsgDiskReq:
		return "DISK_REQ"
	case MsgDiskResp:
		return "DISK_RESP"
	case MsgPing:
		return "PING"
	case MsgPong:
		return "PONG"
	case MsgError:
		return "ERROR"
	default:
		return fmt.Sprintf("UNKNOWN(0x%02x)", t)
	}
}

// ErrName — имя кода ошибки.
func ErrName(code uint16) string {
	switch code {
	case ErrAuthFailed:
		return "AUTH_FAILED"
	case ErrNotFound:
		return "NOT_FOUND"
	case ErrPermissionDenied:
		return "PERMISSION_DENIED"
	case ErrIOError:
		return "IO_ERROR"
	case ErrDiskFull:
		return "DISK_FULL"
	case ErrBadOffset:
		return "BAD_OFFSET"
	case ErrBusy:
		return "BUSY"
	case ErrProtocol:
		return "PROTOCOL_ERROR"
	case ErrSizeMismatch:
		return "SIZE_MISMATCH"
	case ErrUnsupported:
		return "UNSUPPORTED"
	default:
		return fmt.Sprintf("ERR(%d)", code)
	}
}

// ProtoError — ошибка протокола, несущая код ERROR.
type ProtoError struct {
	Code uint16
	Msg  string
}

func (e *ProtoError) Error() string {
	return fmt.Sprintf("%s: %s", ErrName(e.Code), e.Msg)
}

// Errorf создаёт *ProtoError с заданным кодом.
func Errorf(code uint16, format string, a ...any) *ProtoError {
	return &ProtoError{Code: code, Msg: fmt.Sprintf(format, a...)}
}

// WriteFrame пишет один кадр: [версия][тип][длина uint64 BE][payload].
func WriteFrame(w io.Writer, msgType byte, payload []byte) error {
	var hdr [HeaderSize]byte
	hdr[0] = ProtoVersion
	hdr[1] = msgType
	binary.BigEndian.PutUint64(hdr[2:], uint64(len(payload)))
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	if len(payload) > 0 {
		if _, err := w.Write(payload); err != nil {
			return err
		}
	}
	return nil
}

// ReadFrame читает один кадр, ограничивая длину payload значением maxFrame
// (защита от переполнения, 5.2). Возвращает тип и полезную нагрузку.
func ReadFrame(r io.Reader, maxFrame uint64) (byte, []byte, error) {
	var hdr [HeaderSize]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return 0, nil, err
	}
	if hdr[0] != ProtoVersion {
		return hdr[1], nil, Errorf(ErrProtocol, "unsupported proto version %d", hdr[0])
	}
	msgType := hdr[1]
	length := binary.BigEndian.Uint64(hdr[2:])
	if length > maxFrame {
		return msgType, nil, Errorf(ErrProtocol, "frame too large: %d > %d", length, maxFrame)
	}
	var payload []byte
	if length > 0 {
		payload = make([]byte, length)
		if _, err := io.ReadFull(r, payload); err != nil {
			return msgType, nil, err
		}
	}
	return msgType, payload, nil
}

// Builder — построитель полезной нагрузки (big-endian).
type Builder struct {
	buf []byte
}

func (b *Builder) U8(v uint8) { b.buf = append(b.buf, v) }
func (b *Builder) U16(v uint16) {
	b.buf = binary.BigEndian.AppendUint16(b.buf, v)
}
func (b *Builder) U32(v uint32) {
	b.buf = binary.BigEndian.AppendUint32(b.buf, v)
}
func (b *Builder) U64(v uint64) {
	b.buf = binary.BigEndian.AppendUint64(b.buf, v)
}
func (b *Builder) I64(v int64) { b.U64(uint64(v)) }
func (b *Builder) Str(s string) {
	b.U32(uint32(len(s)))
	b.buf = append(b.buf, s...)
}
func (b *Builder) Raw(p []byte)  { b.buf = append(b.buf, p...) }
func (b *Builder) Bytes() []byte { return b.buf }

// Scanner — разбор полезной нагрузки. Любая ошибка границ фиксируется в Err().
type Scanner struct {
	b   []byte
	pos int
	err error
}

func NewScanner(b []byte) *Scanner { return &Scanner{b: b} }

func (s *Scanner) need(n int) bool {
	if s.err != nil {
		return false
	}
	if s.pos+n > len(s.b) {
		s.err = Errorf(ErrProtocol, "payload underflow: need %d at %d of %d", n, s.pos, len(s.b))
		return false
	}
	return true
}

func (s *Scanner) U8() uint8 {
	if !s.need(1) {
		return 0
	}
	v := s.b[s.pos]
	s.pos++
	return v
}

func (s *Scanner) U16() uint16 {
	if !s.need(2) {
		return 0
	}
	v := binary.BigEndian.Uint16(s.b[s.pos:])
	s.pos += 2
	return v
}

func (s *Scanner) U32() uint32 {
	if !s.need(4) {
		return 0
	}
	v := binary.BigEndian.Uint32(s.b[s.pos:])
	s.pos += 4
	return v
}

func (s *Scanner) U64() uint64 {
	if !s.need(8) {
		return 0
	}
	v := binary.BigEndian.Uint64(s.b[s.pos:])
	s.pos += 8
	return v
}

func (s *Scanner) I64() int64 { return int64(s.U64()) }

func (s *Scanner) Str() string {
	n := s.U32()
	if !s.need(int(n)) {
		return ""
	}
	v := string(s.b[s.pos : s.pos+int(n)])
	s.pos += int(n)
	return v
}

func (s *Scanner) Err() error { return s.err }
