// Package store (schema.go) — DDL схемы SQLite (раздел 17 ТЗ).
package store

const schemaVersion = 1

const schemaSQL = `
-- индекс файлов хранилища
CREATE TABLE IF NOT EXISTS files (
  relpath       TEXT PRIMARY KEY,
  relpath_norm  TEXT NOT NULL,
  size          INTEGER NOT NULL,
  mtime         INTEGER NOT NULL,
  updated_at    INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_files_norm ON files(relpath_norm);

-- временная таблица списка клиента (очищается каждый проход)
CREATE TABLE IF NOT EXISTS client_files (
  relpath       TEXT,
  relpath_norm  TEXT,
  size          INTEGER,
  mtime         INTEGER
);
CREATE INDEX IF NOT EXISTS idx_client_norm ON client_files(relpath_norm);

-- очередь передачи (персистентная)
CREATE TABLE IF NOT EXISTS tasks (
  id          INTEGER PRIMARY KEY,
  relpath     TEXT NOT NULL,
  kind        TEXT NOT NULL,
  size        INTEGER NOT NULL,
  "offset"    INTEGER NOT NULL DEFAULT 0,
  status      TEXT NOT NULL,
  attempts    INTEGER NOT NULL DEFAULT 0,
  last_error  TEXT,
  cycle_id    INTEGER,
  created_at  INTEGER NOT NULL,
  updated_at  INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_tasks_status ON tasks(status);
CREATE INDEX IF NOT EXISTS idx_tasks_relpath ON tasks(relpath, kind, status);

-- корзина
CREATE TABLE IF NOT EXISTS trash (
  id          INTEGER PRIMARY KEY,
  relpath     TEXT NOT NULL,
  version     INTEGER NOT NULL DEFAULT 0,
  size        INTEGER NOT NULL,
  trashed_at  INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_trash_time ON trash(trashed_at);
CREATE INDEX IF NOT EXISTS idx_trash_relpath ON trash(relpath, version);

-- циклы и статистика
CREATE TABLE IF NOT EXISTS cycles (
  id               INTEGER PRIMARY KEY,
  started_at       INTEGER NOT NULL,
  finished_at      INTEGER,
  status           TEXT,
  passes_used      INTEGER,
  downloaded_files INTEGER DEFAULT 0,
  downloaded_bytes INTEGER DEFAULT 0,
  changed_files    INTEGER DEFAULT 0,
  trashed_files    INTEGER DEFAULT 0,
  purged_files     INTEGER DEFAULT 0,
  skipped_files    INTEGER DEFAULT 0,
  error_summary    TEXT
);

-- события/алерты для агрегации
CREATE TABLE IF NOT EXISTS events (
  id          INTEGER PRIMARY KEY,
  cycle_id    INTEGER,
  severity    TEXT,
  type        TEXT,
  relpath     TEXT,
  message     TEXT,
  created_at  INTEGER NOT NULL,
  sent        INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_events_sent ON events(sent);

-- метаданные/флаги
CREATE TABLE IF NOT EXISTS meta ( key TEXT PRIMARY KEY, value TEXT );
`
