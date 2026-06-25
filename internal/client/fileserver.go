// Package client (fileserver.go) — отдача содержимого файлов с offset (GET) и
// приём восстановления (PUT) с докачкой, проверкой размера и атомарностью
// (разделы 5.4, 9, 10, 13 ТЗ).
package client

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"time"

	"backuper/internal/diskinfo"
	"backuper/internal/protocol"
	"backuper/internal/tlsconn"
)

func mapOpenErr(err error) uint16 {
	switch {
	case os.IsNotExist(err):
		return protocol.ErrNotFound
	case os.IsPermission(err):
		return protocol.ErrPermissionDenied
	default:
		return protocol.ErrIOError
	}
}

func absI64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

type restoreMeta struct {
	Relpath string `json:"relpath"`
	Total   uint64 `json:"total"`
	Mtime   int64  `json:"mtime"`
}

func (s *Server) restorePartPaths(relpath string) (part, meta string) {
	h := sha1.Sum([]byte(protocol.CleanRel(relpath)))
	base := filepath.Join(s.cfg.RestoreTempDir, hex.EncodeToString(h[:]))
	return base + ".part", base + ".meta"
}

func readRestoreMeta(path string) (restoreMeta, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return restoreMeta{}, false
	}
	var m restoreMeta
	if json.Unmarshal(b, &m) != nil {
		return restoreMeta{}, false
	}
	return m, true
}

// handleGet отдаёт содержимое файла с заданного offset (5.4).
func (s *Server) handleGet(ctx context.Context, conn *tlsconn.Conn, req protocol.GetReq) error {
	full, err := protocol.SafeJoin(s.cfg.BackupDir, req.Path)
	if err != nil {
		return conn.SendError(protocol.ErrProtocol, "GET path: %v", err)
	}
	f, err := os.Open(full)
	if err != nil {
		return conn.SendError(mapOpenErr(err), "%s: %v", req.Path, err)
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return conn.SendError(protocol.ErrIOError, "stat %s: %v", req.Path, err)
	}
	if st.IsDir() {
		return conn.SendError(protocol.ErrNotFound, "%s — директория", req.Path)
	}
	total := uint64(st.Size())
	mtime := st.ModTime().UnixNano()
	if req.Offset > total {
		return conn.SendError(protocol.ErrBadOffset, "offset %d > размера %d", req.Offset, total)
	}
	if err := conn.WriteMsg(protocol.MsgGetResp, protocol.GetResp{Status: 0, TotalSize: total, Mtime: mtime}.Encode()); err != nil {
		return err
	}
	if req.Offset > 0 {
		if _, err := f.Seek(int64(req.Offset), io.SeekStart); err != nil {
			_ = conn.SendError(protocol.ErrIOError, "seek: %v", err)
			return err
		}
	}
	buf := make([]byte, s.cfg.ChunkSize)
	remaining := int64(total) - int64(req.Offset)
	for remaining > 0 {
		toRead := int64(len(buf))
		if toRead > remaining {
			toRead = remaining
		}
		n, rerr := f.Read(buf[:toRead])
		if n > 0 {
			if err := s.lim.wait(ctx, n); err != nil {
				return err
			}
			if err := conn.WriteMsg(protocol.MsgFileData, buf[:n]); err != nil {
				return err
			}
			remaining -= int64(n)
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			_ = conn.SendError(protocol.ErrIOError, "чтение %s: %v", req.Path, rerr)
			return rerr
		}
	}
	return conn.WriteMsg(protocol.MsgFileEnd, nil)
}

// handleDisk сообщает свободное место диска источника (5.4 DISK_RESP).
func (s *Server) handleDisk(conn *tlsconn.Conn) error {
	u, err := diskinfo.Get(s.cfg.BackupDir)
	if err != nil {
		return conn.SendError(protocol.ErrIOError, "diskinfo: %v", err)
	}
	msg := protocol.DiskResp{Mounts: []protocol.MountInfo{{Mount: s.cfg.BackupDir, Total: u.Total, Free: u.Free}}}
	return conn.WriteMsg(protocol.MsgDiskResp, msg.Encode())
}

// handlePut принимает файл восстановления (PUT) с докачкой и проверкой размера (13).
// PUT_RESP используется дважды: сначала resume_offset, затем финальный status.
func (s *Server) handlePut(ctx context.Context, conn *tlsconn.Conn, req protocol.PutReq) error {
	full, err := protocol.SafeJoin(s.cfg.BackupDir, req.Path)
	if err != nil {
		return conn.SendError(protocol.ErrProtocol, "PUT path: %v", err)
	}
	if err := os.MkdirAll(s.cfg.RestoreTempDir, 0o755); err != nil {
		return conn.SendError(protocol.ErrIOError, "temp: %v", err)
	}
	part, meta := s.restorePartPaths(req.Path)

	var resume uint64
	if st, err := os.Stat(part); err == nil {
		resume = uint64(st.Size())
	}
	if resume > 0 {
		pm, ok := readRestoreMeta(meta)
		changed := !ok || pm.Total != req.TotalSize || absI64(pm.Mtime-req.Mtime) > s.cfg.MtimeToleranceNS
		if changed || resume > req.TotalSize {
			os.Remove(part)
			os.Remove(meta)
			resume = 0
		}
	}

	if err := conn.WriteMsg(protocol.MsgPutResp, protocol.PutResp{Status: 0, ResumeOffset: resume}.Encode()); err != nil {
		return err
	}
	b, _ := json.Marshal(restoreMeta{Relpath: req.Path, Total: req.TotalSize, Mtime: req.Mtime})
	_ = os.WriteFile(meta, b, 0o600)

	f, err := os.OpenFile(part, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return conn.SendError(protocol.ErrIOError, "open part: %v", err)
	}
	if err := f.Truncate(int64(resume)); err != nil {
		f.Close()
		return conn.SendError(protocol.ErrIOError, "truncate: %v", err)
	}
	if _, err := f.Seek(int64(resume), io.SeekStart); err != nil {
		f.Close()
		return conn.SendError(protocol.ErrIOError, "seek: %v", err)
	}

	written := int64(resume)
	fail := func(code uint16, format string, a ...any) error {
		f.Close()
		return conn.SendError(code, format, a...)
	}
	for {
		mt, data, err := conn.ReadMsg()
		if err != nil {
			f.Close()
			return err
		}
		switch mt {
		case protocol.MsgFileData:
			if err := s.lim.wait(ctx, len(data)); err != nil {
				f.Close()
				return err
			}
			n, werr := f.Write(data)
			written += int64(n)
			if werr != nil {
				return fail(protocol.ErrIOError, "запись: %v", werr)
			}
			if uint64(written) > req.TotalSize {
				f.Close()
				os.Remove(part)
				os.Remove(meta)
				return conn.WriteMsg(protocol.MsgPutResp, protocol.PutResp{Status: 1}.Encode())
			}
		case protocol.MsgFileEnd:
			goto done
		case protocol.MsgError:
			f.Close()
			return nil // инициатор прервал передачу
		default:
			return fail(protocol.ErrProtocol, "неожиданный кадр %s", protocol.MsgName(mt))
		}
	}
done:
	if err := f.Sync(); err != nil {
		f.Close()
		return conn.SendError(protocol.ErrIOError, "fsync: %v", err)
	}
	f.Close()
	st, err := os.Stat(part)
	if err != nil || uint64(st.Size()) != req.TotalSize {
		os.Remove(part)
		os.Remove(meta)
		return conn.WriteMsg(protocol.MsgPutResp, protocol.PutResp{Status: 1}.Encode())
	}
	if err := os.MkdirAll(filepath.Dir(full), s.cfg.SaveDirPerms); err != nil {
		return conn.SendError(protocol.ErrIOError, "mkdir: %v", err)
	}
	if err := os.Rename(part, full); err != nil {
		return conn.SendError(protocol.ErrIOError, "rename: %v", err)
	}
	_ = os.Chmod(full, s.cfg.SaveFilePerms)
	mt := time.Unix(0, req.Mtime)
	_ = os.Chtimes(full, mt, mt)
	os.Remove(meta)
	s.log.Info("restore", "восстановлен %s (%d Б)", req.Path, req.TotalSize)
	s.log.Audit("restore", req.Path, int64(req.TotalSize), "ok", 1, 0)
	return conn.WriteMsg(protocol.MsgPutResp, protocol.PutResp{Status: 0}.Encode())
}
