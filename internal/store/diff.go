// Package store (diff.go) — алгоритм сравнения (раздел 8 ТЗ) средствами SQL:
// новые/изменённые → очередь задач (INSERT…SELECT), удалённые у клиента →
// постраничная выдача для переноса в корзину. Память не растёт от числа файлов.
package store

// TrashCandidate — файл хранилища, отсутствующий у клиента (к переносу в корзину).
type TrashCandidate struct {
	Relpath string
	Size    int64
}

// DownloadCandidate — файл к скачиванию (для dry-run превью).
type DownloadCandidate struct {
	Relpath string
	Size    int64
	Mtime   int64
	IsNew   bool
}

// CountNewChanged считает новые и изменённые файлы относительно индекса (8, 7.2).
func (s *Store) CountNewChanged(tolNS int64) (newCount, changedCount int64, err error) {
	row := s.db.QueryRow(`
		SELECT
		  COALESCE(SUM(CASE WHEN f.relpath_norm IS NULL THEN 1 ELSE 0 END),0),
		  COALESCE(SUM(CASE WHEN f.relpath_norm IS NOT NULL
		      AND (f.size<>c.size OR ABS(f.mtime-c.mtime)>?) THEN 1 ELSE 0 END),0)
		FROM client_files c
		LEFT JOIN files f ON f.relpath_norm=c.relpath_norm`, tolNS)
	err = row.Scan(&newCount, &changedCount)
	return
}

// EnqueueDiffDownloads ставит в очередь все новые/изменённые файлы одним
// SQL-оператором, пропуская уже стоящие в очереди (pending/in_progress) и уже
// пропущенные в этом же цикле (чтобы проходы сходились). Возвращает число добавленных.
func (s *Store) EnqueueDiffDownloads(cycleID, tolNS, now int64) (int64, error) {
	res, err := s.db.Exec(`
		INSERT INTO tasks(relpath,kind,size,"offset",status,attempts,cycle_id,created_at,updated_at)
		SELECT c.relpath,'download',c.size,0,'pending',0,?,?,?
		FROM client_files c
		LEFT JOIN files f ON f.relpath_norm=c.relpath_norm
		WHERE (f.relpath_norm IS NULL OR f.size<>c.size OR ABS(f.mtime-c.mtime)>?)
		  AND NOT EXISTS (
		      SELECT 1 FROM tasks t
		      WHERE t.relpath=c.relpath AND t.kind='download'
		        AND (t.status IN ('pending','in_progress')
		             OR (t.status='skipped' AND t.cycle_id=?)))`,
		cycleID, now, now, tolNS, cycleID)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// CountClientCollisions — число нормализованных путей, встречающихся в списке
// клиента более одного раза (коллизия по регистру, 5.6).
func (s *Store) CountClientCollisions() (int64, error) {
	var n int64
	err := s.db.QueryRow(`
		SELECT COUNT(*) FROM (
		  SELECT relpath_norm FROM client_files GROUP BY relpath_norm HAVING COUNT(*)>1)`).Scan(&n)
	return n, err
}

// ListClientCollisionExamples возвращает примеры конфликтующих путей.
func (s *Store) ListClientCollisionExamples(limit int) ([]string, error) {
	rows, err := s.db.Query(`
		SELECT relpath FROM client_files
		WHERE relpath_norm IN (
		  SELECT relpath_norm FROM client_files GROUP BY relpath_norm HAVING COUNT(*)>1)
		ORDER BY relpath_norm, relpath LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// DedupClientFiles удаляет дубликаты в client_files, оставляя по одной записи на
// нормализованный путь (наименьший rowid). Возвращает число удалённых.
func (s *Store) DedupClientFiles() (int64, error) {
	res, err := s.db.Exec(`
		DELETE FROM client_files
		WHERE rowid NOT IN (SELECT MIN(rowid) FROM client_files GROUP BY relpath_norm)`)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// CountToTrash — число файлов хранилища, отсутствующих у клиента (8, 11.5).
func (s *Store) CountToTrash() (int64, error) {
	var n int64
	err := s.db.QueryRow(`
		SELECT COUNT(*) FROM files f
		LEFT JOIN client_files c ON c.relpath_norm=f.relpath_norm
		WHERE c.relpath_norm IS NULL`).Scan(&n)
	return n, err
}

// SelectTrashBatch выдаёт очередную порцию файлов к переносу в корзину.
// Постранично за счёт того, что обработанные удаляются из files (DeleteFileByNorm).
func (s *Store) SelectTrashBatch(limit int) ([]TrashCandidate, error) {
	rows, err := s.db.Query(`
		SELECT f.relpath, f.size FROM files f
		LEFT JOIN client_files c ON c.relpath_norm=f.relpath_norm
		WHERE c.relpath_norm IS NULL
		LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TrashCandidate
	for rows.Next() {
		var t TrashCandidate
		if err := rows.Scan(&t.Relpath, &t.Size); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// ListDownloadPreview выдаёт до limit файлов к скачиванию (dry-run, раздел 23.1).
func (s *Store) ListDownloadPreview(tolNS int64, limit int) ([]DownloadCandidate, error) {
	rows, err := s.db.Query(`
		SELECT c.relpath, c.size, c.mtime, (f.relpath_norm IS NULL)
		FROM client_files c
		LEFT JOIN files f ON f.relpath_norm=c.relpath_norm
		WHERE (f.relpath_norm IS NULL OR f.size<>c.size OR ABS(f.mtime-c.mtime)>?)
		LIMIT ?`, tolNS, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DownloadCandidate
	for rows.Next() {
		var d DownloadCandidate
		if err := rows.Scan(&d.Relpath, &d.Size, &d.Mtime, &d.IsNew); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// ListTrashPreview выдаёт до limit файлов к переносу в корзину (dry-run).
func (s *Store) ListTrashPreview(limit int) ([]TrashCandidate, error) {
	return s.SelectTrashBatch(limit)
}
