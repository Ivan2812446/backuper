// Package store (trashstore.go) — записи корзины и их версии (раздел 11, 17 ТЗ).
package store

import "database/sql"

// TrashItem — запись корзины.
type TrashItem struct {
	ID        int64
	Relpath   string
	Version   int
	Size      int64
	TrashedAt int64
}

// HasCanonicalTrash — есть ли в корзине каноническая (version=0) запись для relpath.
func (s *Store) HasCanonicalTrash(relpath string) (bool, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM trash WHERE relpath=? AND version=0`, relpath).Scan(&n)
	return n > 0, err
}

// MaxTrashVersion — максимальный номер версии для relpath (0, если записей нет).
func (s *Store) MaxTrashVersion(relpath string) (int, error) {
	var v sql.NullInt64
	err := s.db.QueryRow(`SELECT MAX(version) FROM trash WHERE relpath=?`, relpath).Scan(&v)
	if err != nil {
		return 0, err
	}
	return int(v.Int64), nil
}

// BumpCanonicalToVersion переводит каноническую запись relpath в версию newVersion (11.2).
func (s *Store) BumpCanonicalToVersion(relpath string, newVersion int) error {
	_, err := s.db.Exec(`UPDATE trash SET version=? WHERE relpath=? AND version=0`, newVersion, relpath)
	return err
}

// InsertTrash добавляет запись корзины и возвращает id.
func (s *Store) InsertTrash(relpath string, version int, size, trashedAt int64) (int64, error) {
	res, err := s.db.Exec(
		`INSERT INTO trash(relpath,version,size,trashed_at) VALUES(?,?,?,?)`,
		relpath, version, size, trashedAt)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// SelectTrashDue выдаёт порцию записей корзины со сроком < cutoff (11.3).
func (s *Store) SelectTrashDue(cutoff int64, limit int) ([]TrashItem, error) {
	rows, err := s.db.Query(
		`SELECT id,relpath,version,size,trashed_at FROM trash WHERE trashed_at<? ORDER BY id LIMIT ?`,
		cutoff, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TrashItem
	for rows.Next() {
		var t TrashItem
		if err := rows.Scan(&t.ID, &t.Relpath, &t.Version, &t.Size, &t.TrashedAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// DeleteTrash удаляет запись корзины по id.
func (s *Store) DeleteTrash(id int64) error {
	_, err := s.db.Exec(`DELETE FROM trash WHERE id=?`, id)
	return err
}

// CountTrash — число записей в корзине.
func (s *Store) CountTrash() (int64, error) {
	var n int64
	err := s.db.QueryRow(`SELECT COUNT(*) FROM trash`).Scan(&n)
	return n, err
}
