// Package db provides SQLite storage for scan metadata and results.
package db

import (
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/yuriy-kovalchuk/yk-helm-update-checker/internal/constants"
	_ "modernc.org/sqlite"
)

// DB wraps the SQLite database connection.
type DB struct {
	conn *sql.DB
}

// schema contains the DDL for initializing the database.
const schema = `
-- Scan metadata
CREATE TABLE IF NOT EXISTS scans (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    started_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    completed_at  DATETIME,
    status        TEXT NOT NULL DEFAULT 'running',
    error_message TEXT,
    result_count  INTEGER DEFAULT 0,
    scope         TEXT NOT NULL,
    trigger       TEXT NOT NULL DEFAULT 'manual'
);

-- Scan results (references scans)
CREATE TABLE IF NOT EXISTS results (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    scan_id          INTEGER NOT NULL REFERENCES scans(id) ON DELETE CASCADE,
    source           TEXT NOT NULL,
    chart            TEXT NOT NULL,
    dependency       TEXT NOT NULL,
    type             TEXT NOT NULL,
    protocol         TEXT NOT NULL,
    current_version  TEXT NOT NULL,
    latest_version   TEXT,
    scope            TEXT NOT NULL,
    update_available BOOLEAN NOT NULL DEFAULT 0,
    checked_at       DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_scans_started_at ON scans(started_at DESC);
CREATE INDEX IF NOT EXISTS idx_results_scan_id ON results(scan_id);

-- Enable foreign key enforcement
PRAGMA foreign_keys = ON;
`

// Open opens the SQLite database at the given path, creating the file and
// directory structure if needed. It also runs schema migrations.
func Open(path string) (*DB, error) {
	// Ensure parent directory exists
	dir := filepath.Dir(path)
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create db directory: %w", err)
		}
	}

	// Open database with WAL mode for better concurrency
	dsn := fmt.Sprintf("file:%s?_journal_mode=WAL&_busy_timeout=%d&_foreign_keys=ON", path, constants.DBBusyTimeout.Milliseconds())
	conn, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}

	// Limit connections for SQLite
	conn.SetMaxOpenConns(constants.DBMaxOpenConns)

	// Run migrations
	if _, err := conn.Exec(schema); err != nil {
		conn.Close()
		return nil, fmt.Errorf("run schema migrations: %w", err)
	}

	slog.Info("database opened", "path", path)
	return &DB{conn: conn}, nil
}

// Close closes the database connection.
func (db *DB) Close() error {
	return db.conn.Close()
}
