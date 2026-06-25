// Package server (lock.go) — защита от двойного запуска через lock/pid-файл
// (раздел 17, NFR-5 ТЗ).
package server

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Lock — захваченная блокировка одиночного запуска.
type Lock struct {
	path string
	f    *os.File
}

// AcquireLock берёт эксклюзивную блокировку; при наличии устаревшего lock от
// мёртвого процесса — перехватывает.
func AcquireLock(path string) (*Lock, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		if !os.IsExist(err) {
			return nil, err
		}
		data, _ := os.ReadFile(path)
		if pid, perr := strconv.Atoi(strings.TrimSpace(string(data))); perr == nil && pid > 0 && processAlive(pid) {
			return nil, fmt.Errorf("экземпляр уже запущен (pid %d, lock %s)", pid, path)
		}
		_ = os.Remove(path)
		f, err = os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err != nil {
			return nil, err
		}
	}
	if _, err := f.WriteString(strconv.Itoa(os.Getpid())); err != nil {
		f.Close()
		return nil, err
	}
	_ = f.Sync()
	return &Lock{path: path, f: f}, nil
}

// Release освобождает блокировку.
func (l *Lock) Release() {
	if l == nil {
		return
	}
	_ = l.f.Close()
	_ = os.Remove(l.path)
}
