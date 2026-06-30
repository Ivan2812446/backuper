// Package protocol (messages.go) — типизированные сообщения протокола
// и их кодирование/декодирование в полезную нагрузку кадра (5.4 ТЗ).
package protocol

// FileEntry — запись списка файлов клиента (LIST_BATCH).
type FileEntry struct {
	Path  string
	Size  uint64
	Mtime int64
}

// MountInfo — информация о точке монтирования (DISK_RESP).
type MountInfo struct {
	Mount string
	Total uint64
	Free  uint64
}

// --- AUTH_REQ (0x01) ---

type AuthReq struct {
	ProtoVersion uint8
	Password     string
}

func (m AuthReq) Encode() []byte {
	var b Builder
	b.U8(m.ProtoVersion)
	b.Str(m.Password)
	return b.Bytes()
}

func ParseAuthReq(p []byte) (AuthReq, error) {
	s := NewScanner(p)
	m := AuthReq{ProtoVersion: s.U8(), Password: s.Str()}
	return m, s.Err()
}

// --- AUTH_RESP (0x02) ---

type AuthResp struct {
	Password string
	Status   uint8 // 0 = ok
}

func (m AuthResp) Encode() []byte {
	var b Builder
	b.Str(m.Password)
	b.U8(m.Status)
	return b.Bytes()
}

func ParseAuthResp(p []byte) (AuthResp, error) {
	s := NewScanner(p)
	m := AuthResp{Password: s.Str(), Status: s.U8()}
	return m, s.Err()
}

// --- LIST_REQ (0x10) ---

type ListReq struct{ Root string }

func (m ListReq) Encode() []byte {
	var b Builder
	b.Str(m.Root)
	return b.Bytes()
}

func ParseListReq(p []byte) (ListReq, error) {
	s := NewScanner(p)
	m := ListReq{Root: s.Str()}
	return m, s.Err()
}

// --- LIST_BATCH (0x11) ---

type ListBatch struct {
	IsLast  bool
	Entries []FileEntry
}

func (m ListBatch) Encode() []byte {
	var b Builder
	if m.IsLast {
		b.U8(1)
	} else {
		b.U8(0)
	}
	b.U32(uint32(len(m.Entries)))
	for _, e := range m.Entries {
		b.Str(e.Path)
		b.U64(e.Size)
		b.I64(e.Mtime)
	}
	return b.Bytes()
}

func ParseListBatch(p []byte) (ListBatch, error) {
	s := NewScanner(p)
	m := ListBatch{IsLast: s.U8() == 1}
	count := s.U32()
	if s.Err() != nil {
		return m, s.Err()
	}
	m.Entries = make([]FileEntry, 0, count)
	for i := uint32(0); i < count; i++ {
		e := FileEntry{Path: s.Str(), Size: s.U64(), Mtime: s.I64()}
		if s.Err() != nil {
			return m, s.Err()
		}
		m.Entries = append(m.Entries, e)
	}
	return m, s.Err()
}

// --- GET_REQ (0x20) ---

type GetReq struct {
	Path   string
	Offset uint64
}

func (m GetReq) Encode() []byte {
	var b Builder
	b.Str(m.Path)
	b.U64(m.Offset)
	return b.Bytes()
}

func ParseGetReq(p []byte) (GetReq, error) {
	s := NewScanner(p)
	m := GetReq{Path: s.Str(), Offset: s.U64()}
	return m, s.Err()
}

// --- GET_RESP (0x21) ---

type GetResp struct {
	Status    uint8
	TotalSize uint64
	Mtime     int64
}

func (m GetResp) Encode() []byte {
	var b Builder
	b.U8(m.Status)
	b.U64(m.TotalSize)
	b.I64(m.Mtime)
	return b.Bytes()
}

func ParseGetResp(p []byte) (GetResp, error) {
	s := NewScanner(p)
	m := GetResp{Status: s.U8(), TotalSize: s.U64(), Mtime: s.I64()}
	return m, s.Err()
}

// --- PUT_REQ (0x30) ---

type PutReq struct {
	Path      string
	TotalSize uint64
	Offset    uint64
	Mtime     int64
}

func (m PutReq) Encode() []byte {
	var b Builder
	b.Str(m.Path)
	b.U64(m.TotalSize)
	b.U64(m.Offset)
	b.I64(m.Mtime)
	return b.Bytes()
}

func ParsePutReq(p []byte) (PutReq, error) {
	s := NewScanner(p)
	m := PutReq{Path: s.Str(), TotalSize: s.U64(), Offset: s.U64(), Mtime: s.I64()}
	return m, s.Err()
}

// --- PUT_RESP (0x31) ---

type PutResp struct {
	Status       uint8
	ResumeOffset uint64
}

func (m PutResp) Encode() []byte {
	var b Builder
	b.U8(m.Status)
	b.U64(m.ResumeOffset)
	return b.Bytes()
}

func ParsePutResp(p []byte) (PutResp, error) {
	s := NewScanner(p)
	m := PutResp{Status: s.U8(), ResumeOffset: s.U64()}
	return m, s.Err()
}

// --- GET_DELTA (0x24) ---

// DeltaReq — запрос дельты: путь, размер блока и хэши блоков СТАРОЙ версии (sha256).
type DeltaReq struct {
	Path      string
	BlockSize uint32
	Hashes    [][32]byte
}

func (m DeltaReq) Encode() []byte {
	var b Builder
	b.Str(m.Path)
	b.U32(m.BlockSize)
	b.U32(uint32(len(m.Hashes)))
	for i := range m.Hashes {
		b.Raw(m.Hashes[i][:])
	}
	return b.Bytes()
}

func ParseDeltaReq(p []byte) (DeltaReq, error) {
	s := NewScanner(p)
	m := DeltaReq{Path: s.Str(), BlockSize: s.U32()}
	count := s.U32()
	if s.Err() != nil {
		return m, s.Err()
	}
	m.Hashes = make([][32]byte, 0, count)
	for i := uint32(0); i < count; i++ {
		raw := s.Bytes(32)
		if s.Err() != nil {
			return m, s.Err()
		}
		var h [32]byte
		copy(h[:], raw)
		m.Hashes = append(m.Hashes, h)
	}
	return m, s.Err()
}

// --- DELTA_RESP (0x25) ---

type DeltaResp struct {
	Status    uint8
	TotalSize uint64
	Mtime     int64
}

func (m DeltaResp) Encode() []byte {
	var b Builder
	b.U8(m.Status)
	b.U64(m.TotalSize)
	b.I64(m.Mtime)
	return b.Bytes()
}

func ParseDeltaResp(p []byte) (DeltaResp, error) {
	s := NewScanner(p)
	m := DeltaResp{Status: s.U8(), TotalSize: s.U64(), Mtime: s.I64()}
	return m, s.Err()
}

// --- DISK_RESP (0x41) ---

type DiskResp struct {
	Mounts []MountInfo
}

func (m DiskResp) Encode() []byte {
	var b Builder
	b.U8(uint8(len(m.Mounts)))
	for _, mi := range m.Mounts {
		b.Str(mi.Mount)
		b.U64(mi.Total)
		b.U64(mi.Free)
	}
	return b.Bytes()
}

func ParseDiskResp(p []byte) (DiskResp, error) {
	s := NewScanner(p)
	count := s.U8()
	m := DiskResp{}
	if s.Err() != nil {
		return m, s.Err()
	}
	m.Mounts = make([]MountInfo, 0, count)
	for i := uint8(0); i < count; i++ {
		mi := MountInfo{Mount: s.Str(), Total: s.U64(), Free: s.U64()}
		if s.Err() != nil {
			return m, s.Err()
		}
		m.Mounts = append(m.Mounts, mi)
	}
	return m, s.Err()
}

// --- ERROR (0xFF) ---

type ErrorMsg struct {
	Code    uint16
	Message string
}

func (m ErrorMsg) Encode() []byte {
	var b Builder
	b.U16(m.Code)
	b.Str(m.Message)
	return b.Bytes()
}

func ParseErrorMsg(p []byte) (ErrorMsg, error) {
	s := NewScanner(p)
	m := ErrorMsg{Code: s.U16(), Message: s.Str()}
	return m, s.Err()
}
