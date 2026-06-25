// Package store (restorequeries.go) — выборки для восстановления (раздел 13 ТЗ):
// постраничный обход индекса хранилища и mtime файла у клиента.
package store

import "database/sql"

// FileRow — запись индекса для восстановления.
type FileRow struct {
	Relpath string
	Size    int64
	Mtime   int64
}

// ListFilesPage возвращает до limit файлов индекса с relpath > after (постранично).
// prefix="" — весь индекс; иначе ровно prefix или поддерево prefix/*.
func (s *Store) ListFilesPage(prefix, after string, limit int) ([]FileRow, error) {
	var rows *sql.Rows
	var err error
	if prefix == "" {
		rows, err = s.db.Query(
			`SELECT relpath,size,mtime FROM files WHERE relpath>? ORDER BY relpath LIMIT ?`,
			after, limit)
	} else {
		rows, err = s.db.Query(
			`SELECT relpath,size,mtime FROM files WHERE (relpath=? OR relpath LIKE ?) AND relpath>? ORDER BY relpath LIMIT ?`,
			prefix, prefix+"/%", after, limit)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []FileRow
	for rows.Next() {
		var r FileRow
		if err := rows.Scan(&r.Relpath, &r.Size, &r.Mtime); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ClientFileMtime возвращает mtime файла из текущего списка клиента по норм-пути.
func (s *Store) ClientFileMtime(norm string) (int64, bool, error) {
	var mt int64
	err := s.db.QueryRow(`SELECT mtime FROM client_files WHERE relpath_norm=? LIMIT 1`, norm).Scan(&mt)
	if err == sql.ErrNoRows {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return mt, true, nil
}
