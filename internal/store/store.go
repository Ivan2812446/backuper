// Package store — состояние сервера в SQLite (WAL): индекс хранилища, временный
// список клиента, очередь задач, корзина, циклы, события, meta (раздел 17 ТЗ).
//
// Доступ сериализован одним соединением (SetMaxOpenConns(1)); все операции
// спроектированы без вложенных запросов поверх открытого курсора/транзакции,
// что исключает SQLITE_BUSY и взаимоблокировки. Большие множества обрабатываются
// SQL-операциями (INSERT…SELECT) и постранично — индекс не загружается в память.
package store

import (
	"database/sql"
	"fmt"
	"net/url"

	_ "modernc.org/sqlite"
)

// Store — обёртка над базой состояния.
type Store struct {
	db *sql.DB
}

// Open открывает (создаёт) базу, включает WAL и применяет схему.
func Open(path string) (*Store, error) {
	dsn := "file:" + url.PathEscape(path) +
		"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(10000)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(0)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, err
	}
	s := &Store{db: db}
	if _, err := db.Exec(schemaSQL); err != nil {
		db.Close()
		return nil, fmt.Errorf("применение схемы: %w", err)
	}
	if _, ok, _ := s.GetMeta("schema_version"); !ok {
		_ = s.SetMeta("schema_version", fmt.Sprintf("%d", schemaVersion))
	}
	return s, nil
}

// Close закрывает базу.
func (s *Store) Close() error { return s.db.Close() }

// --- meta ---

// GetMeta возвращает значение флага.
func (s *Store) GetMeta(key string) (string, bool, error) {
	var v string
	err := s.db.QueryRow(`SELECT value FROM meta WHERE key=?`, key).Scan(&v)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return v, true, nil
}

// SetMeta устанавливает значение флага.
func (s *Store) SetMeta(key, value string) error {
	_, err := s.db.Exec(
		`INSERT INTO meta(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`,
		key, value)
	return err
}

// Initialized — был ли выполнен первый запуск (12).
func (s *Store) Initialized() (bool, error) {
	v, ok, err := s.GetMeta("initialized")
	return ok && v == "1", err
}

// SetInitialized помечает завершение первого запуска.
func (s *Store) SetInitialized() error { return s.SetMeta("initialized", "1") }

// --- индекс файлов хранилища ---

// CountFiles — число записей индекса.
func (s *Store) CountFiles() (int64, error) {
	var n int64
	err := s.db.QueryRow(`SELECT COUNT(*) FROM files`).Scan(&n)
	return n, err
}

// GetFile возвращает запись индекса по нормализованному пути.
func (s *Store) GetFile(relpathNorm string) (relpath string, size, mtime int64, ok bool, err error) {
	err = s.db.QueryRow(`SELECT relpath,size,mtime FROM files WHERE relpath_norm=?`, relpathNorm).
		Scan(&relpath, &size, &mtime)
	if err == sql.ErrNoRows {
		return "", 0, 0, false, nil
	}
	if err != nil {
		return "", 0, 0, false, err
	}
	return relpath, size, mtime, true, nil
}

// UpsertFile добавляет/обновляет запись индекса.
func (s *Store) UpsertFile(relpath, relpathNorm string, size, mtime, updatedAt int64) error {
	_, err := s.db.Exec(
		`INSERT INTO files(relpath,relpath_norm,size,mtime,updated_at) VALUES(?,?,?,?,?)
		 ON CONFLICT(relpath) DO UPDATE SET relpath_norm=excluded.relpath_norm,
		   size=excluded.size, mtime=excluded.mtime, updated_at=excluded.updated_at`,
		relpath, relpathNorm, size, mtime, updatedAt)
	return err
}

// DeleteFileByNorm удаляет запись индекса по нормализованному пути.
func (s *Store) DeleteFileByNorm(relpathNorm string) error {
	_, err := s.db.Exec(`DELETE FROM files WHERE relpath_norm=?`, relpathNorm)
	return err
}

// FileWriter — батч-вставка в индекс в одной транзакции (первый запуск, 1 млн файлов).
type FileWriter struct {
	tx    *sql.Tx
	stmt  *sql.Stmt
	db    *sql.DB
	n     int
	batch int
}

// BeginFileIndex открывает батч-писатель индекса.
func (s *Store) BeginFileIndex() (*FileWriter, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	stmt, err := tx.Prepare(
		`INSERT INTO files(relpath,relpath_norm,size,mtime,updated_at) VALUES(?,?,?,?,?)
		 ON CONFLICT(relpath) DO UPDATE SET relpath_norm=excluded.relpath_norm,
		   size=excluded.size, mtime=excluded.mtime, updated_at=excluded.updated_at`)
	if err != nil {
		tx.Rollback()
		return nil, err
	}
	return &FileWriter{tx: tx, stmt: stmt, db: s.db, batch: 5000}, nil
}

// Add вставляет запись; раз в batch строк фиксирует транзакцию и открывает новую.
func (w *FileWriter) Add(relpath, relpathNorm string, size, mtime, updatedAt int64) error {
	if _, err := w.stmt.Exec(relpath, relpathNorm, size, mtime, updatedAt); err != nil {
		return err
	}
	w.n++
	if w.n%w.batch == 0 {
		if err := w.tx.Commit(); err != nil {
			return err
		}
		tx, err := w.db.Begin()
		if err != nil {
			return err
		}
		stmt, err := tx.Prepare(
			`INSERT INTO files(relpath,relpath_norm,size,mtime,updated_at) VALUES(?,?,?,?,?)
			 ON CONFLICT(relpath) DO UPDATE SET relpath_norm=excluded.relpath_norm,
			   size=excluded.size, mtime=excluded.mtime, updated_at=excluded.updated_at`)
		if err != nil {
			tx.Rollback()
			return err
		}
		w.tx, w.stmt = tx, stmt
	}
	return nil
}

// Commit фиксирует остаток.
func (w *FileWriter) Commit() error { return w.tx.Commit() }

// Rollback откатывает текущую (незафиксированную) транзакцию, освобождая соединение.
func (w *FileWriter) Rollback() error { return w.tx.Rollback() }

// Count — сколько записей добавлено.
func (w *FileWriter) Count() int { return w.n }

// --- временный список клиента ---

// ResetClientFiles очищает временную таблицу перед новым проходом (8).
func (s *Store) ResetClientFiles() error {
	_, err := s.db.Exec(`DELETE FROM client_files`)
	return err
}

// ClientWriter — батч-вставка списка клиента.
type ClientWriter struct {
	tx    *sql.Tx
	stmt  *sql.Stmt
	db    *sql.DB
	n     int
	batch int
}

// BeginClientFiles открывает батч-писатель списка клиента.
func (s *Store) BeginClientFiles() (*ClientWriter, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	stmt, err := tx.Prepare(`INSERT INTO client_files(relpath,relpath_norm,size,mtime) VALUES(?,?,?,?)`)
	if err != nil {
		tx.Rollback()
		return nil, err
	}
	return &ClientWriter{tx: tx, stmt: stmt, db: s.db, batch: 5000}, nil
}

// Add вставляет запись списка клиента.
func (w *ClientWriter) Add(relpath, relpathNorm string, size, mtime int64) error {
	if _, err := w.stmt.Exec(relpath, relpathNorm, size, mtime); err != nil {
		return err
	}
	w.n++
	if w.n%w.batch == 0 {
		if err := w.tx.Commit(); err != nil {
			return err
		}
		tx, err := w.db.Begin()
		if err != nil {
			return err
		}
		stmt, err := tx.Prepare(`INSERT INTO client_files(relpath,relpath_norm,size,mtime) VALUES(?,?,?,?)`)
		if err != nil {
			tx.Rollback()
			return err
		}
		w.tx, w.stmt = tx, stmt
	}
	return nil
}

// Commit фиксирует остаток.
func (w *ClientWriter) Commit() error { return w.tx.Commit() }

// Rollback откатывает текущую (незафиксированную) транзакцию, освобождая соединение.
func (w *ClientWriter) Rollback() error { return w.tx.Rollback() }

// Count — число записей списка клиента.
func (w *ClientWriter) Count() int { return w.n }

// CountClientFiles — число записей в текущем списке клиента.
func (s *Store) CountClientFiles() (int64, error) {
	var n int64
	err := s.db.QueryRow(`SELECT COUNT(*) FROM client_files`).Scan(&n)
	return n, err
}
