//go:build windows

// Package diskinfo (windows) — свободное место через GetDiskFreeSpaceEx.
package diskinfo

import "golang.org/x/sys/windows"

func get(path string) (Usage, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return Usage{}, err
	}
	var freeAvail, total, totalFree uint64
	if err := windows.GetDiskFreeSpaceEx(p, &freeAvail, &total, &totalFree); err != nil {
		return Usage{}, err
	}
	return Usage{Total: total, Free: freeAvail}, nil
}
