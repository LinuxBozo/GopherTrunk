package storage

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// ConvLockout is one conventional scan-list channel the operator has
// locked out at runtime. Keyed by frequency rather than list index so it
// survives the operator reordering, inserting or removing channels in the
// config between restarts: the lockout re-applies to whichever channel
// carries the same frequency, and a frequency that is no longer in the
// list is simply ignored (kept, so it re-applies if the channel returns).
type ConvLockout struct {
	FrequencyHz uint32    `json:"frequency_hz"`
	Label       string    `json:"label,omitempty"`
	LockedAt    time.Time `json:"locked_at"`
}

// ConvLockoutStore persists conventional scanner lockouts across daemon
// restarts — what a hardware scanner does with its lockout memory, and
// what the runtime-only map used to lose on every restart.
type ConvLockoutStore struct {
	db *DB
}

// NewConvLockoutStore returns a store backed by the given DB.
func NewConvLockoutStore(db *DB) (*ConvLockoutStore, error) {
	if db == nil {
		return nil, errors.New("storage/convlockouts: DB is required")
	}
	return &ConvLockoutStore{db: db}, nil
}

// Set records freqHz as locked out (idempotent; refreshes the label).
func (s *ConvLockoutStore) Set(ctx context.Context, freqHz uint32, label string) error {
	if freqHz == 0 {
		return errors.New("storage/convlockouts: frequency is required")
	}
	const q = `
INSERT INTO conv_lockouts (frequency_hz, label, locked_at) VALUES (?, ?, ?)
ON CONFLICT(frequency_hz) DO UPDATE SET label = excluded.label`
	if _, err := s.db.SQL().ExecContext(ctx, q, freqHz, label, time.Now().UnixNano()); err != nil {
		return fmt.Errorf("storage/convlockouts: set: %w", err)
	}
	return nil
}

// Clear removes freqHz from the lockout set. Clearing an unknown
// frequency is not an error.
func (s *ConvLockoutStore) Clear(ctx context.Context, freqHz uint32) error {
	if _, err := s.db.SQL().ExecContext(ctx,
		`DELETE FROM conv_lockouts WHERE frequency_hz = ?`, freqHz); err != nil {
		return fmt.Errorf("storage/convlockouts: clear: %w", err)
	}
	return nil
}

// List returns every persisted lockout, oldest first.
func (s *ConvLockoutStore) List(ctx context.Context) ([]ConvLockout, error) {
	rows, err := s.db.SQL().QueryContext(ctx,
		`SELECT frequency_hz, label, locked_at FROM conv_lockouts ORDER BY locked_at, frequency_hz`)
	if err != nil {
		return nil, fmt.Errorf("storage/convlockouts: list: %w", err)
	}
	defer rows.Close()
	var out []ConvLockout
	for rows.Next() {
		var l ConvLockout
		var at int64
		if err := rows.Scan(&l.FrequencyHz, &l.Label, &at); err != nil {
			return nil, fmt.Errorf("storage/convlockouts: scan: %w", err)
		}
		l.LockedAt = time.Unix(0, at)
		out = append(out, l)
	}
	return out, rows.Err()
}
