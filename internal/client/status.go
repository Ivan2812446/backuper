// Package client (status.go) — статус-файл слушателя для команды `status` (раздел 19.3 ТЗ).
package client

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"
)

// Status — снимок состояния слушателя.
type Status struct {
	PID           int    `json:"pid"`
	StartedAt     string `json:"started_at"`
	ListenAddr    string `json:"listen_addr"`
	AcceptedTotal int64  `json:"accepted_total"`
	LastRemote    string `json:"last_remote"`
	LastResult    string `json:"last_result"`
	LastAt        string `json:"last_at"`
}

type statusState struct {
	mu   sync.Mutex
	path string
	st   Status
}

func newStatus(path string) *statusState {
	return &statusState{
		path: path,
		st:   Status{PID: os.Getpid(), StartedAt: time.Now().Format(time.RFC3339)},
	}
}

func (s *statusState) setListen(host string, port int) {
	s.mu.Lock()
	s.st.ListenAddr = fmt.Sprintf("%s:%d", host, port)
	s.mu.Unlock()
}

func (s *statusState) record(remote, result string) {
	s.mu.Lock()
	s.st.AcceptedTotal++
	s.st.LastRemote = remote
	s.st.LastResult = result
	s.st.LastAt = time.Now().Format(time.RFC3339)
	s.mu.Unlock()
	s.flush()
}

func (s *statusState) flush() {
	if s.path == "" {
		return
	}
	s.mu.Lock()
	b, _ := json.MarshalIndent(s.st, "", "  ")
	s.mu.Unlock()
	tmp := s.path + ".tmp"
	if os.WriteFile(tmp, b, 0o644) == nil {
		_ = os.Rename(tmp, s.path)
	}
}

// ReadStatus читает статус-файл (для команды status).
func ReadStatus(path string) (Status, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Status{}, err
	}
	var st Status
	if err := json.Unmarshal(b, &st); err != nil {
		return Status{}, err
	}
	return st, nil
}
