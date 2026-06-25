// Package store (tasks.go) — персистентная очередь передачи (раздел 9.4, 17 ТЗ).
package store

import "database/sql"

// Task — задача передачи.
type Task struct {
	ID       int64
	Relpath  string
	Kind     string // download | restore
	Size     int64
	Offset   int64
	Status   string // pending|in_progress|done|failed|skipped
	Attempts int
	LastErr  string
	CycleID  int64
}

// InsertTask добавляет задачу и возвращает её id.
func (s *Store) InsertTask(relpath, kind string, size, offset int64, status string, cycleID, now int64) (int64, error) {
	res, err := s.db.Exec(
		`INSERT INTO tasks(relpath,kind,size,"offset",status,attempts,cycle_id,created_at,updated_at)
		 VALUES(?,?,?,?,?,0,?,?,?)`,
		relpath, kind, size, offset, status, cycleID, now, now)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func scanTasks(rows *sql.Rows) ([]Task, error) {
	defer rows.Close()
	var out []Task
	for rows.Next() {
		var t Task
		var lastErr sql.NullString
		var cycleID sql.NullInt64
		if err := rows.Scan(&t.ID, &t.Relpath, &t.Kind, &t.Size, &t.Offset,
			&t.Status, &t.Attempts, &lastErr, &cycleID); err != nil {
			return nil, err
		}
		t.LastErr = lastErr.String
		t.CycleID = cycleID.Int64
		out = append(out, t)
	}
	return out, rows.Err()
}

const taskCols = `id,relpath,kind,size,"offset",status,attempts,last_error,cycle_id`

// ListRunnableTasks возвращает задачи к исполнению указанного вида
// (pending — готовые; in_progress оставлены для возобновления, 9.3).
func (s *Store) ListRunnableTasks(kind string) ([]Task, error) {
	rows, err := s.db.Query(
		`SELECT `+taskCols+` FROM tasks WHERE kind=? AND status IN ('pending','in_progress') ORDER BY id`, kind)
	if err != nil {
		return nil, err
	}
	return scanTasks(rows)
}

// ListRunnableTasksPage постранично выдаёт задачи к исполнению (id > afterID),
// чтобы не загружать всю очередь (до 1 млн) в память (NFR-1).
func (s *Store) ListRunnableTasksPage(kind string, afterID int64, limit int) ([]Task, error) {
	rows, err := s.db.Query(
		`SELECT `+taskCols+` FROM tasks WHERE kind=? AND status IN ('pending','in_progress') AND id>? ORDER BY id LIMIT ?`,
		kind, afterID, limit)
	if err != nil {
		return nil, err
	}
	return scanTasks(rows)
}

// ListTasksByStatus возвращает задачи с заданным статусом (для отчёта/статуса).
func (s *Store) ListTasksByStatus(status string, limit int) ([]Task, error) {
	rows, err := s.db.Query(
		`SELECT `+taskCols+` FROM tasks WHERE status=? ORDER BY id LIMIT ?`, status, limit)
	if err != nil {
		return nil, err
	}
	return scanTasks(rows)
}

// CountTasks — число задач с заданным статусом.
func (s *Store) CountTasks(status string) (int64, error) {
	var n int64
	err := s.db.QueryRow(`SELECT COUNT(*) FROM tasks WHERE status=?`, status).Scan(&n)
	return n, err
}

// ResetStaleInProgress переводит прерванные in_progress → pending (сохраняя offset)
// при старте службы (9.3, NFR-3).
func (s *Store) ResetStaleInProgress(now int64) error {
	_, err := s.db.Exec(`UPDATE tasks SET status='pending', updated_at=? WHERE status='in_progress'`, now)
	return err
}

// MarkTaskInProgress помечает задачу выполняемой.
func (s *Store) MarkTaskInProgress(id, now int64) error {
	_, err := s.db.Exec(`UPDATE tasks SET status='in_progress', updated_at=? WHERE id=?`, now, id)
	return err
}

// UpdateTaskOffset сохраняет прогресс докачки (9.3).
func (s *Store) UpdateTaskOffset(id, offset, now int64) error {
	_, err := s.db.Exec(`UPDATE tasks SET "offset"=?, updated_at=? WHERE id=?`, offset, now, id)
	return err
}

// MarkTaskDone помечает задачу завершённой.
func (s *Store) MarkTaskDone(id, now int64) error {
	_, err := s.db.Exec(`UPDATE tasks SET status='done', updated_at=? WHERE id=?`, now, id)
	return err
}

// IncTaskAttempt фиксирует неудачу: attempts++, status=failed; возвращает новое число попыток.
func (s *Store) IncTaskAttempt(id int64, errMsg string, now int64) (int, error) {
	if _, err := s.db.Exec(
		`UPDATE tasks SET status='failed', attempts=attempts+1, last_error=?, updated_at=? WHERE id=?`,
		errMsg, now, id); err != nil {
		return 0, err
	}
	var n int
	err := s.db.QueryRow(`SELECT attempts FROM tasks WHERE id=?`, id).Scan(&n)
	return n, err
}

// SetTaskStatus меняет статус задачи (pending для повтора, skipped после исчерпания).
func (s *Store) SetTaskStatus(id int64, status string, now int64) error {
	_, err := s.db.Exec(`UPDATE tasks SET status=?, updated_at=? WHERE id=?`, status, now, id)
	return err
}

// SetTaskResetOffset сбрасывает offset в 0 (перекачка с нуля, 9.3).
func (s *Store) SetTaskResetOffset(id, now int64) error {
	_, err := s.db.Exec(`UPDATE tasks SET "offset"=0, updated_at=? WHERE id=?`, now, id)
	return err
}

// DeleteDoneTasks удаляет завершённые задачи (очистка очереди в конце цикла).
func (s *Store) DeleteDoneTasks() error {
	_, err := s.db.Exec(`DELETE FROM tasks WHERE status='done'`)
	return err
}

// DeleteTasksByKind удаляет все задачи указанного вида (например restore после завершения).
func (s *Store) DeleteTasksByKind(kind string) error {
	_, err := s.db.Exec(`DELETE FROM tasks WHERE kind=?`, kind)
	return err
}

// DeleteTasksByStatus удаляет задачи с заданным статусом (очистка skipped/failed в конце цикла).
func (s *Store) DeleteTasksByStatus(status string) error {
	_, err := s.db.Exec(`DELETE FROM tasks WHERE status=?`, status)
	return err
}
