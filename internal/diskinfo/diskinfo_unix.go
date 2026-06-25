//go:build !windows

// Package diskinfo (unix) — свободное место через syscall Statfs.
package diskinfo

import "golang.org/x/sys/unix"

func get(path string) (Usage, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return Usage{}, err
	}
	bsize := uint64(st.Bsize)
	return Usage{
		Total: st.Blocks * bsize,
		Free:  st.Bavail * bsize, // доступно непривилегированному процессу
	}, nil
}
