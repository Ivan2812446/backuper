package protocol

import (
	"bytes"
	"io"
	"testing"
)

func TestFrameRoundTrip(t *testing.T) {
	cases := [][]byte{
		nil,
		[]byte("hello"),
		bytes.Repeat([]byte{0xAB}, 1<<16),
	}
	for _, payload := range cases {
		var buf bytes.Buffer
		if err := WriteFrame(&buf, MsgGetReq, payload); err != nil {
			t.Fatalf("WriteFrame: %v", err)
		}
		mt, got, err := ReadFrame(&buf, 1<<20)
		if err != nil {
			t.Fatalf("ReadFrame: %v", err)
		}
		if mt != MsgGetReq {
			t.Fatalf("type = %#x, want %#x", mt, MsgGetReq)
		}
		if !bytes.Equal(got, payload) {
			t.Fatalf("payload mismatch: got %d bytes, want %d", len(got), len(payload))
		}
	}
}

func TestReadFrameMaxFrame(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteFrame(&buf, MsgListBatch, bytes.Repeat([]byte{1}, 100)); err != nil {
		t.Fatal(err)
	}
	_, _, err := ReadFrame(&buf, 10) // лимит меньше payload
	if err == nil {
		t.Fatal("ожидалась ошибка превышения лимита кадра")
	}
	pe, ok := err.(*ProtoError)
	if !ok || pe.Code != ErrProtocol {
		t.Fatalf("ожидался ProtoError/PROTOCOL_ERROR, получено %v", err)
	}
}

func TestReadFrameBadVersion(t *testing.T) {
	// версия 2 вместо 1
	raw := []byte{2, MsgPing, 0, 0, 0, 0, 0, 0, 0, 0}
	_, _, err := ReadFrame(bytes.NewReader(raw), 1<<20)
	if pe, ok := err.(*ProtoError); !ok || pe.Code != ErrProtocol {
		t.Fatalf("ожидался PROTOCOL_ERROR по версии, получено %v", err)
	}
}

func TestReadFrameTruncated(t *testing.T) {
	var buf bytes.Buffer
	WriteFrame(&buf, MsgGetReq, []byte("0123456789"))
	b := buf.Bytes()[:HeaderSize+3] // обрезаем payload
	_, _, err := ReadFrame(bytes.NewReader(b), 1<<20)
	if err == nil {
		t.Fatal("ожидалась ошибка на усечённом кадре")
	}
}

func TestBuilderScannerRoundTrip(t *testing.T) {
	var b Builder
	b.U8(0xFE)
	b.U16(0x1234)
	b.U32(0xDEADBEEF)
	b.U64(0x0102030405060708)
	b.I64(-42)
	b.Str("привет")
	s := NewScanner(b.Bytes())
	if v := s.U8(); v != 0xFE {
		t.Fatalf("U8=%#x", v)
	}
	if v := s.U16(); v != 0x1234 {
		t.Fatalf("U16=%#x", v)
	}
	if v := s.U32(); v != 0xDEADBEEF {
		t.Fatalf("U32=%#x", v)
	}
	if v := s.U64(); v != 0x0102030405060708 {
		t.Fatalf("U64=%#x", v)
	}
	if v := s.I64(); v != -42 {
		t.Fatalf("I64=%d", v)
	}
	if v := s.Str(); v != "привет" {
		t.Fatalf("Str=%q", v)
	}
	if err := s.Err(); err != nil {
		t.Fatalf("Err=%v", err)
	}
}

func TestScannerUnderflow(t *testing.T) {
	s := NewScanner([]byte{0x01, 0x02}) // 2 байта
	s.U32()                             // запросим 4 -> underflow
	if s.Err() == nil {
		t.Fatal("ожидался underflow")
	}
	// последующие чтения безопасны и возвращают нули
	if s.U8() != 0 {
		t.Fatal("после ошибки должно возвращаться 0")
	}
}

func TestMessagesRoundTrip(t *testing.T) {
	t.Run("AuthReq", func(t *testing.T) {
		m := AuthReq{ProtoVersion: 1, Password: "pw-секрет"}
		got, err := ParseAuthReq(m.Encode())
		if err != nil || got != m {
			t.Fatalf("got %+v err %v", got, err)
		}
	})
	t.Run("AuthResp", func(t *testing.T) {
		m := AuthResp{Password: "p", Status: 0}
		got, err := ParseAuthResp(m.Encode())
		if err != nil || got != m {
			t.Fatalf("got %+v err %v", got, err)
		}
	})
	t.Run("ListReq", func(t *testing.T) {
		m := ListReq{Root: "docs/sub"}
		got, err := ParseListReq(m.Encode())
		if err != nil || got != m {
			t.Fatalf("got %+v err %v", got, err)
		}
	})
	t.Run("ListBatch", func(t *testing.T) {
		m := ListBatch{IsLast: true, Entries: []FileEntry{
			{Path: "a/b.txt", Size: 123, Mtime: 1700000000000000000},
			{Path: "c.pdf", Size: 0, Mtime: -1},
		}}
		got, err := ParseListBatch(m.Encode())
		if err != nil {
			t.Fatal(err)
		}
		if !got.IsLast || len(got.Entries) != 2 || got.Entries[0] != m.Entries[0] || got.Entries[1] != m.Entries[1] {
			t.Fatalf("got %+v", got)
		}
	})
	t.Run("GetReqResp", func(t *testing.T) {
		req := GetReq{Path: "x", Offset: 1 << 40}
		gr, err := ParseGetReq(req.Encode())
		if err != nil || gr != req {
			t.Fatalf("GetReq got %+v err %v", gr, err)
		}
		resp := GetResp{Status: 0, TotalSize: 9999, Mtime: 12345}
		gp, err := ParseGetResp(resp.Encode())
		if err != nil || gp != resp {
			t.Fatalf("GetResp got %+v err %v", gp, err)
		}
	})
	t.Run("PutReqResp", func(t *testing.T) {
		req := PutReq{Path: "p", TotalSize: 5, Offset: 2, Mtime: 7}
		gp, err := ParsePutReq(req.Encode())
		if err != nil || gp != req {
			t.Fatalf("PutReq got %+v err %v", gp, err)
		}
		resp := PutResp{Status: 1, ResumeOffset: 3}
		gr, err := ParsePutResp(resp.Encode())
		if err != nil || gr != resp {
			t.Fatalf("PutResp got %+v err %v", gr, err)
		}
	})
	t.Run("DiskResp", func(t *testing.T) {
		m := DiskResp{Mounts: []MountInfo{{Mount: "/data", Total: 100, Free: 40}}}
		got, err := ParseDiskResp(m.Encode())
		if err != nil || len(got.Mounts) != 1 || got.Mounts[0] != m.Mounts[0] {
			t.Fatalf("got %+v err %v", got, err)
		}
	})
	t.Run("Error", func(t *testing.T) {
		m := ErrorMsg{Code: ErrSizeMismatch, Message: "не совпал размер"}
		got, err := ParseErrorMsg(m.Encode())
		if err != nil || got != m {
			t.Fatalf("got %+v err %v", got, err)
		}
	})
}

func TestNamesNonEmpty(t *testing.T) {
	if MsgName(MsgFileData) != "FILE_DATA" {
		t.Fatal("MsgName")
	}
	if MsgName(0x99) == "" {
		t.Fatal("unknown msg name empty")
	}
	if ErrName(ErrAuthFailed) != "AUTH_FAILED" {
		t.Fatal("ErrName")
	}
}

// убедимся, что Builder реально big-endian
func TestBigEndian(t *testing.T) {
	var b Builder
	b.U32(1)
	want := []byte{0, 0, 0, 1}
	if !bytes.Equal(b.Bytes(), want) {
		t.Fatalf("U32(1) = %v, want big-endian %v", b.Bytes(), want)
	}
}

var _ io.Reader = (*bytes.Reader)(nil)
