// MDC1200 log writer — drains KindMDC1200Message events off the
// shared bus and writes one row per decoded signaling burst to the
// SQLite mdc1200_log table. Mirrors dsclog.go / aprslog.go.
package storage

import (
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/MattCheramie/GopherTrunk/internal/events"
)

// MDC1200Message is one persisted decoded MDC1200 burst.
type MDC1200Message struct {
	ID         int64     `json:"id"`
	ReceivedAt time.Time `json:"received_at"`
	Op         uint8     `json:"op"`
	Arg        uint8     `json:"arg"`
	UnitID     uint16    `json:"unit_id"`
	Operation  string    `json:"operation"` // "PTT ID" | "Emergency" | ... ("" if unknown)
	Body       string    `json:"body"`
	RawHex     string    `json:"raw_hex"`
	CRCOK      bool      `json:"crc_ok"`
	// Serial and FrequencyHz name the receiver that decoded the burst —
	// the mdc1200.channels entry, or the scanner.conventional channel the
	// scanner was dwelling on (#1220) — so an operator can tell which
	// channel produced an ID. Zero on rows written before they existed.
	Serial      string `json:"serial"`
	FrequencyHz uint32 `json:"frequency_hz"`
}

// MDC1200Log drains KindMDC1200Message events until ctx cancels or the
// bus closes.
type MDC1200Log struct {
	*eventLog[MDC1200Message]
	db *DB
}

// NewMDC1200Log wires the log to the bus. Subscription happens at
// construction so events published before Run() begins aren't lost.
func NewMDC1200Log(db *DB, bus *events.Bus, logger *slog.Logger) (*MDC1200Log, error) {
	if db == nil {
		return nil, errors.New("storage/mdc1200log: DB is required")
	}
	m := &MDC1200Log{db: db}
	el, err := newEventLog[MDC1200Message](bus, logger, events.KindMDC1200Message, "mdc1200log", m.insert)
	if err != nil {
		return nil, err
	}
	m.eventLog = el
	return m, nil
}

func (m *MDC1200Log) insert(msg MDC1200Message) error {
	at := msg.ReceivedAt
	if at.IsZero() {
		at = time.Now()
	}
	crcOK := 0
	if msg.CRCOK {
		crcOK = 1
	}
	_, err := m.db.SQL().Exec(
		`INSERT INTO mdc1200_log
		 (received_at, op, arg, unit_id, operation, body, raw_hex, crc_ok, serial, frequency_hz)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		at.UnixNano(), msg.Op, msg.Arg, msg.UnitID, msg.Operation,
		msg.Body, msg.RawHex, crcOK, msg.Serial, int64(msg.FrequencyHz),
	)
	return err
}

// Recent returns the most recent bursts, newest first, capped at
// limit. limit ≤ 0 picks 200; limit > 5000 caps at 5000.
func (m *MDC1200Log) Recent(limit int) ([]MDC1200Message, error) {
	if limit <= 0 {
		limit = 200
	}
	if limit > 5000 {
		limit = 5000
	}
	rows, err := m.db.SQL().Query(
		`SELECT id, received_at, op, arg, unit_id, operation, body,
		        raw_hex, crc_ok, serial, frequency_hz
		 FROM mdc1200_log ORDER BY received_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("storage/mdc1200log: query: %w", err)
	}
	defer rows.Close()
	var out []MDC1200Message
	for rows.Next() {
		var (
			msg   MDC1200Message
			ns    int64
			op    int
			arg   int
			unit  int
			crcOK int
			freq  int64
		)
		if err := rows.Scan(&msg.ID, &ns, &op, &arg, &unit, &msg.Operation,
			&msg.Body, &msg.RawHex, &crcOK, &msg.Serial, &freq); err != nil {
			return nil, fmt.Errorf("storage/mdc1200log: scan: %w", err)
		}
		msg.ReceivedAt = time.Unix(0, ns)
		msg.Op = uint8(op)
		msg.Arg = uint8(arg)
		msg.UnitID = uint16(unit)
		msg.CRCOK = crcOK != 0
		msg.FrequencyHz = uint32(freq)
		out = append(out, msg)
	}
	return out, rows.Err()
}
