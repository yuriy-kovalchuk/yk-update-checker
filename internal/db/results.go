package db

import (
	"fmt"
	"time"
)

// Result represents a single dependency check result.
type Result struct {
	ID              int64     `json:"-"`
	ScanID          int64     `json:"-"`
	Source          string    `json:"source"`
	Chart           string    `json:"chart"`
	Dependency      string    `json:"dependency"`
	Type            string    `json:"type"`
	Protocol        string    `json:"protocol"`
	CurrentVersion  string    `json:"current_version"`
	LatestVersion   string    `json:"latest_version"`
	Scope           string    `json:"scope"`
	UpdateAvailable bool      `json:"update_available"`
	CheckedAt       time.Time `json:"-"`
}

// InsertResults inserts multiple results in a single transaction.
func (db *DB) InsertResults(results []Result) error {
	tx, err := db.conn.Begin()
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare(
		`INSERT INTO results (scan_id, source, chart, dependency, type, protocol,
		                      current_version, latest_version, scope, update_available, checked_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
	)
	if err != nil {
		return fmt.Errorf("prepare statement: %w", err)
	}
	defer stmt.Close()

	for _, r := range results {
		_, err := stmt.Exec(
			r.ScanID, r.Source, r.Chart, r.Dependency, r.Type, r.Protocol,
			r.CurrentVersion, r.LatestVersion, r.Scope, r.UpdateAvailable, r.CheckedAt,
		)
		if err != nil {
			return fmt.Errorf("insert result: %w", err)
		}
	}

	return tx.Commit()
}

// GetLatestResults retrieves results from the most recent completed scan.
func (db *DB) GetLatestResults() ([]Result, error) {
	rows, err := db.conn.Query(
		`SELECT r.id, r.scan_id, r.source, r.chart, r.dependency, r.type, r.protocol,
		        r.current_version, r.latest_version, r.scope, r.update_available, r.checked_at
		 FROM results r
		 WHERE r.scan_id = (
		     SELECT id FROM scans WHERE status = ? ORDER BY started_at DESC LIMIT 1
		 )
		 ORDER BY r.source, r.chart, r.dependency`,
		ScanStatusCompleted,
	)
	if err != nil {
		return nil, fmt.Errorf("query latest results: %w", err)
	}
	defer rows.Close()

	return scanResultRows(rows)
}

func scanResultRows(rows interface {
	Next() bool
	Scan(dest ...any) error
	Err() error
}) ([]Result, error) {
	results := make([]Result, 0)
	for rows.Next() {
		var r Result
		var latest *string
		if err := rows.Scan(
			&r.ID, &r.ScanID, &r.Source, &r.Chart, &r.Dependency, &r.Type, &r.Protocol,
			&r.CurrentVersion, &latest, &r.Scope, &r.UpdateAvailable, &r.CheckedAt,
		); err != nil {
			return nil, fmt.Errorf("scan row: %w", err)
		}
		if latest != nil {
			r.LatestVersion = *latest
		}
		results = append(results, r)
	}
	return results, rows.Err()
}
