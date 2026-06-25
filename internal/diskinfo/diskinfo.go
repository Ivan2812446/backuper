// Package diskinfo — свободное место на дисках (раздел 4.3, FR-9 ТЗ).
// Кроссплатформенно: Statfs на Unix, GetDiskFreeSpaceEx на Windows.
package diskinfo

// Usage — общий и доступный объём файловой системы, содержащей путь.
type Usage struct {
	Total uint64
	Free  uint64
}

// UsedPercent — процент занятого места (0..100). 0 при неизвестном Total.
func (u Usage) UsedPercent() float64 {
	if u.Total == 0 {
		return 0
	}
	return float64(u.Total-u.Free) / float64(u.Total) * 100
}

// FreePercent — процент свободного места.
func (u Usage) FreePercent() float64 {
	if u.Total == 0 {
		return 0
	}
	return float64(u.Free) / float64(u.Total) * 100
}

// Get — использование файловой системы, содержащей путь.
// Реализация — в diskinfo_unix.go / diskinfo_windows.go.
func Get(path string) (Usage, error) {
	return get(path)
}
