// Package store (events.go) — события/алерты для агрегации в письма (раздел 15.3, 17 ТЗ).
package store

import (
	"database/sql"
	"strings"
)

// Event — событие для агрегированного алерта.
type Event struct {
	ID        int64
	CycleID   int64
	Severity  string // INFO|WARN|ERROR
	Type      string // auth|io|disk|size|mass_delete|restart|overlap
	Relpath   string
	Message   string
	CreatedAt int64
}

// InsertEvent добавляет событие (по умолчанию неотправленное).
func (s *Store) InsertEvent(cycleID int64, severity, typ, relpath, message string, now int64) (int64, error) {
	var cid any
	if cycleID > 0 {
		cid = cycleID
	}
	res, err := s.db.Exec(
		`INSERT INTO events(cycle_id,severity,type,relpath,message,created_at,sent) VALUES(?,?,?,?,?,?,0)`,
		cid, severity, typ, relpath, message, now)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func scanEvents(rows *sql.Rows) ([]Event, error) {
	defer rows.Close()
	var out []Event
	for rows.Next() {
		var e Event
		var cid sql.NullInt64
		var relpath, msg sql.NullString
		if err := rows.Scan(&e.ID, &cid, &e.Severity, &e.Type, &relpath, &msg, &e.CreatedAt); err != nil {
			return nil, err
		}
		e.CycleID = cid.Int64
		e.Relpath = relpath.String
		e.Message = msg.String
		out = append(out, e)
	}
	return out, rows.Err()
}

// ListUnsentEvents возвращает все неотправленные события.
func (s *Store) ListUnsentEvents() ([]Event, error) {
	rows, err := s.db.Query(
		`SELECT id,cycle_id,severity,type,relpath,message,created_at FROM events WHERE sent=0 ORDER BY id`)
	if err != nil {
		return nil, err
	}
	return scanEvents(rows)
}

// ListEventsForCycle возвращает события цикла (для отчёта).
func (s *Store) ListEventsForCycle(cycleID int64) ([]Event, error) {
	rows, err := s.db.Query(
		`SELECT id,cycle_id,severity,type,relpath,message,created_at FROM events WHERE cycle_id=? ORDER BY id`, cycleID)
	if err != nil {
		return nil, err
	}
	return scanEvents(rows)
}

// CountUnsentEvents — число неотправленных событий.
func (s *Store) CountUnsentEvents() (int64, error) {
	var n int64
	err := s.db.QueryRow(`SELECT COUNT(*) FROM events WHERE sent=0`).Scan(&n)
	return n, err
}

// MarkEventsSent помечает события отправленными.
func (s *Store) MarkEventsSent(ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	ph := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		ph[i] = "?"
		args[i] = id
	}
	_, err := s.db.Exec(`UPDATE events SET sent=1 WHERE id IN (`+strings.Join(ph, ",")+`)`, args...)
	return err
}
