package db

import (
	"database/sql"
	"fmt"
	"time"
)

// ScanStatus represents the state of a scan.
type ScanStatus string

const (
	ScanStatusRunning   ScanStatus = "running"
	ScanStatusCompleted ScanStatus = "completed"
	ScanStatusFailed    ScanStatus = "failed"
)

// ScanTrigger indicates how the scan was initiated.
type ScanTrigger string

const (
	ScanTriggerManual    ScanTrigger = "manual"
	ScanTriggerScheduled ScanTrigger = "scheduled"
)

// Scan represents a scan execution record.
type Scan struct {
	ID               int64       `json:"id"`
	StartedAt        time.Time   `json:"started_at"`
	CompletedAt      *time.Time  `json:"completed_at,omitempty"`
	Status           ScanStatus  `json:"status"`
	ErrorMessage     string      `json:"error,omitempty"`
	ResultCount      int         `json:"result_count"`
	UpdatesAvailable int         `json:"updates_available"`
	Scope            string      `json:"scope"`
	Trigger          ScanTrigger `json:"trigger"`
	DurationSeconds  *float64    `json:"duration_s,omitempty"`
}

// CreateScan creates a new scan record with status "running".
func (db *DB) CreateScan(scope string, trigger ScanTrigger) (int64, error) {
	result, err := db.conn.Exec(
		`INSERT INTO scans (scope, trigger, status) VALUES (?, ?, ?)`,
		scope, trigger, ScanStatusRunning,
	)
	if err != nil {
		return 0, fmt.Errorf("insert scan: %w", err)
	}
	return result.LastInsertId()
}

// CompleteScan marks a scan as completed with the result count.
func (db *DB) CompleteScan(scanID int64, resultCount int) error {
	_, err := db.conn.Exec(
		`UPDATE scans SET status = ?, completed_at = CURRENT_TIMESTAMP, result_count = ? WHERE id = ?`,
		ScanStatusCompleted, resultCount, scanID,
	)
	return err
}

// FailScan marks a scan as failed with an error message.
func (db *DB) FailScan(scanID int64, errMsg string) error {
	_, err := db.conn.Exec(
		`UPDATE scans SET status = ?, completed_at = CURRENT_TIMESTAMP, error_message = ? WHERE id = ?`,
		ScanStatusFailed, errMsg, scanID,
	)
	return err
}

// ListScans returns scans ordered by started_at descending, with updates_available
// computed from the results table. Pass limit <= 0 for no limit.
func (db *DB) ListScans(limit int) ([]Scan, error) {
	query := `SELECT s.id, s.started_at, s.completed_at, s.status, s.error_message,
	                 s.result_count, s.scope, s.trigger,
	                 COALESCE(SUM(r.update_available), 0) AS updates_available
	          FROM scans s
	          LEFT JOIN results r ON r.scan_id = s.id
	          GROUP BY s.id
	          ORDER BY s.started_at DESC`
	if limit > 0 {
		query += fmt.Sprintf(" LIMIT %d", limit)
	}

	rows, err := db.conn.Query(query)
	if err != nil {
		return nil, fmt.Errorf("list scans: %w", err)
	}
	defer rows.Close()

	scans := make([]Scan, 0)
	for rows.Next() {
		var s Scan
		var completedAt sql.NullTime
		var errMsg sql.NullString

		if err := rows.Scan(&s.ID, &s.StartedAt, &completedAt, &s.Status, &errMsg,
			&s.ResultCount, &s.Scope, &s.Trigger, &s.UpdatesAvailable); err != nil {
			return nil, fmt.Errorf("scan row: %w", err)
		}

		if completedAt.Valid {
			s.CompletedAt = &completedAt.Time
			d := completedAt.Time.Sub(s.StartedAt).Seconds()
			s.DurationSeconds = &d
		}
		s.ErrorMessage = errMsg.String

		scans = append(scans, s)
	}

	return scans, rows.Err()
}

// LatestScan returns the most recent scan or nil if none exist.
func (db *DB) LatestScan() (*Scan, error) {
	scans, err := db.ListScans(1)
	if err != nil {
		return nil, err
	}
	if len(scans) == 0 {
		return nil, nil
	}
	return &scans[0], nil
}

// LatestCompletedScan returns the most recent completed scan or nil if none exist.
func (db *DB) LatestCompletedScan() (*Scan, error) {
	rows, err := db.conn.Query(
		`SELECT s.id, s.started_at, s.completed_at, s.status, s.error_message,
		        s.result_count, s.scope, s.trigger,
		        COALESCE(SUM(r.update_available), 0) AS updates_available
		 FROM scans s
		 LEFT JOIN results r ON r.scan_id = s.id
		 WHERE s.status = ?
		 GROUP BY s.id
		 ORDER BY s.started_at DESC
		 LIMIT 1`,
		ScanStatusCompleted,
	)
	if err != nil {
		return nil, fmt.Errorf("get latest completed scan: %w", err)
	}
	defer rows.Close()

	if !rows.Next() {
		return nil, rows.Err()
	}

	var s Scan
	var completedAt sql.NullTime
	var errMsg sql.NullString
	if err := rows.Scan(&s.ID, &s.StartedAt, &completedAt, &s.Status, &errMsg,
		&s.ResultCount, &s.Scope, &s.Trigger, &s.UpdatesAvailable); err != nil {
		return nil, fmt.Errorf("scan row: %w", err)
	}
	if completedAt.Valid {
		s.CompletedAt = &completedAt.Time
		d := completedAt.Time.Sub(s.StartedAt).Seconds()
		s.DurationSeconds = &d
	}
	s.ErrorMessage = errMsg.String
	return &s, nil
}

// IsScanning returns true if there's a scan currently running.
func (db *DB) IsScanning() (bool, error) {
	var count int
	err := db.conn.QueryRow(
		`SELECT COUNT(*) FROM scans WHERE status = ?`,
		ScanStatusRunning,
	).Scan(&count)
	return count > 0, err
}

// FailStuckScans auto-fails scans that have been in "running" state longer than olderThan.
// Returns the number of scans updated.
func (db *DB) FailStuckScans(olderThan time.Duration) (int64, error) {
	cutoff := time.Now().Add(-olderThan)
	result, err := db.conn.Exec(
		`UPDATE scans SET status = ?, completed_at = CURRENT_TIMESTAMP, error_message = ?
		 WHERE status = ? AND started_at < ?`,
		ScanStatusFailed, "timed out: job was likely killed before completing", ScanStatusRunning, cutoff,
	)
	if err != nil {
		return 0, fmt.Errorf("fail stuck scans: %w", err)
	}
	return result.RowsAffected()
}

// DeleteOldScans removes scans older than the given duration, keeping at least minKeep scans.
func (db *DB) DeleteOldScans(olderThan time.Duration, minKeep int) (int64, error) {
	cutoff := time.Now().Add(-olderThan)
	result, err := db.conn.Exec(
		`DELETE FROM scans WHERE started_at < ? AND id NOT IN (
			SELECT id FROM scans ORDER BY started_at DESC LIMIT ?
		)`,
		cutoff, minKeep,
	)
	if err != nil {
		return 0, fmt.Errorf("delete old scans: %w", err)
	}
	return result.RowsAffected()
}
