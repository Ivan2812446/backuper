// Package store (cycles.go) — циклы сверки и их статистика (раздел 17, 15.4 ТЗ).
package store

import "database/sql"

// Cycle — запись цикла сверки и накопленная статистика.
type Cycle struct {
	ID              int64
	StartedAt       int64
	FinishedAt      int64
	Status          string // OK|PARTIAL|FAILED
	PassesUsed      int
	DownloadedFiles int64
	DownloadedBytes int64
	ChangedFiles    int64
	TrashedFiles    int64
	PurgedFiles     int64
	SkippedFiles    int64
	ErrorSummary    string
}

// CreateCycle создаёт запись цикла и возвращает id.
func (s *Store) CreateCycle(startedAt int64) (int64, error) {
	res, err := s.db.Exec(`INSERT INTO cycles(started_at,status) VALUES(?, 'RUNNING')`, startedAt)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// FinalizeCycle сохраняет итоговую статистику цикла.
func (s *Store) FinalizeCycle(c Cycle) error {
	_, err := s.db.Exec(`
		UPDATE cycles SET finished_at=?, status=?, passes_used=?,
		  downloaded_files=?, downloaded_bytes=?, changed_files=?,
		  trashed_files=?, purged_files=?, skipped_files=?, error_summary=?
		WHERE id=?`,
		c.FinishedAt, c.Status, c.PassesUsed,
		c.DownloadedFiles, c.DownloadedBytes, c.ChangedFiles,
		c.TrashedFiles, c.PurgedFiles, c.SkippedFiles, c.ErrorSummary, c.ID)
	return err
}

func scanCycle(row interface{ Scan(...any) error }) (Cycle, bool, error) {
	var c Cycle
	var finished sql.NullInt64
	var status, errSummary sql.NullString
	var passes sql.NullInt64
	err := row.Scan(&c.ID, &c.StartedAt, &finished, &status, &passes,
		&c.DownloadedFiles, &c.DownloadedBytes, &c.ChangedFiles,
		&c.TrashedFiles, &c.PurgedFiles, &c.SkippedFiles, &errSummary)
	if err == sql.ErrNoRows {
		return Cycle{}, false, nil
	}
	if err != nil {
		return Cycle{}, false, err
	}
	c.FinishedAt = finished.Int64
	c.Status = status.String
	c.PassesUsed = int(passes.Int64)
	c.ErrorSummary = errSummary.String
	return c, true, nil
}

const cycleCols = `id,started_at,finished_at,status,passes_used,downloaded_files,downloaded_bytes,changed_files,trashed_files,purged_files,skipped_files,error_summary`

// GetLastCycle возвращает последний цикл (для status).
func (s *Store) GetLastCycle() (Cycle, bool, error) {
	return scanCycle(s.db.QueryRow(`SELECT ` + cycleCols + ` FROM cycles ORDER BY id DESC LIMIT 1`))
}

// GetCycle возвращает цикл по id.
func (s *Store) GetCycle(id int64) (Cycle, bool, error) {
	return scanCycle(s.db.QueryRow(`SELECT `+cycleCols+` FROM cycles WHERE id=?`, id))
}
