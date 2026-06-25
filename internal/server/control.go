// Package server (control.go) — контрольное соединение (раздел 4.2 ТЗ):
// одно TLS-соединение на цикл для LIST/DISK/PING, сериализованное мьютексом.
package server

import (
	"sync"

	"backuper/internal/alert"
	"backuper/internal/logx"
	"backuper/internal/protocol"
	"backuper/internal/store"
	"backuper/internal/tlsconn"
)

type control struct {
	mu   sync.Mutex
	conn *tlsconn.Conn
	log  *logx.Logger
}

func (c *control) close() {
	if c.conn != nil {
		c.conn.Close()
	}
}

// disk запрашивает свободное место диска источника (DISK_REQ/RESP).
func (c *control) disk() (alert.Disk, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.conn.WriteMsg(protocol.MsgDiskReq, nil); err != nil {
		return alert.Disk{}, err
	}
	payload, err := c.conn.ReadExpect(protocol.MsgDiskResp)
	if err != nil {
		return alert.Disk{}, err
	}
	dr, err := protocol.ParseDiskResp(payload)
	if err != nil {
		return alert.Disk{}, err
	}
	if len(dr.Mounts) == 0 {
		return alert.Disk{Name: "клиент"}, nil
	}
	m := dr.Mounts[0]
	return alert.Disk{Name: "клиент:" + m.Mount, Total: m.Total, Free: m.Free}, nil
}

// ping — проверка живости контрольного соединения (5.7).
func (c *control) ping() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.conn.WriteMsg(protocol.MsgPing, nil); err != nil {
		return err
	}
	_, err := c.conn.ReadExpect(protocol.MsgPong)
	return err
}

// loadList запрашивает список клиента и стримит его в client_files батчами (8, 20).
func (c *control) loadList(st *store.Store, root string) (int64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.conn.WriteMsg(protocol.MsgListReq, protocol.ListReq{Root: root}.Encode()); err != nil {
		return 0, err
	}
	if err := st.ResetClientFiles(); err != nil {
		return 0, err
	}
	w, err := st.BeginClientFiles()
	if err != nil {
		return 0, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = w.Rollback()
		}
	}()
	for {
		payload, err := c.conn.ReadExpect(protocol.MsgListBatch)
		if err != nil {
			return 0, err
		}
		batch, err := protocol.ParseListBatch(payload)
		if err != nil {
			return 0, err
		}
		for _, e := range batch.Entries {
			rel := protocol.CleanRel(e.Path)
			if rel == "" {
				continue
			}
			if err := w.Add(rel, protocol.NormKey(rel), int64(e.Size), e.Mtime); err != nil {
				return 0, err
			}
		}
		if batch.IsLast {
			break
		}
	}
	if err := w.Commit(); err != nil {
		return 0, err
	}
	committed = true
	return int64(w.Count()), nil
}
