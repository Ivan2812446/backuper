//go:build !windows

// Package server (lock_unix.go) — проверка живости процесса по сигналу 0.
package server

import (
	"os"
	"syscall"
)

func processAlive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return p.Signal(syscall.Signal(0)) == nil
}
