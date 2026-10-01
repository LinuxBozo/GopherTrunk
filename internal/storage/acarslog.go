// ACARS log writer — drains KindACARSMessage events off the shared bus and
// writes one row per decoded block to the SQLite acars_log table. Mirrors
// fleetsynclog.go.
package storage

import (
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/MattCheramie/GopherTrunk/internal/events"
)

// ACARSMessage is one persisted decoded ACARS block (#1231).
type ACARSMessage struct {
	ID         int64     `json:"id"`
	ReceivedAt time.Time `json:"received_at"`
	Mode       string    `json:"mode"`      // mode character
	Address    string    `json:"address"`   // aircraft registration
	Ack        string    `json:"ack"`       // technical ack; "!" is a NAK
	Label      string    `json:"label"`     // two-character label
	BlockID    string    `json:"block_id"`  // '0'..'9' downlink, letters uplink, "" squitter
	Downlink   bool      `json:"downlink"`  // air-to-ground
	MsgNo      string    `json:"msg_no"`    // downlink message number
	FlightID   string    `json:"flight_id"` // downlink flight identifier
	Text       string    `json:"text"`      // message body
	More       bool      `json:"more"`      // ETB: further blocks follow
	CRCOK      bool      `json:"crc_ok"`    // the block check validated
	Corrected  int       `json:"corrected"` // bits repaired before it validated
	RawHex     string    `json:"raw_hex"`   // received block, mode through BCS
	// Serial and FrequencyHz name the receiver that decoded the block: the
	// scanner's SDR and the scan-list channel.
	Serial      string `json:"serial"`
	FrequencyHz uint32 `json:"frequency_hz"`
}

// ACARSLog drains KindACARSMessage events until ctx cancels or the bus
// closes.
type ACARSLog struct {
	*eventLog[ACARSMessage]
	db *DB
}

// NewACARSLog wires the log to the bus. Subscription happens at
// construction so events published before Run() begins aren't lost.
func NewACARSLog(db *DB, bus *events.Bus, logger *slog.Logger) (*ACARSLog, error) {
	if db == nil {
		return nil, errors.New("storage/acarslog: DB is required")
	}
	a := &ACARSLog{db: db}
	el, err := newEventLog[ACARSMessage](bus, logger, events.KindACARSMessage, "acarslog", a.insert)
	if err != nil {
		return nil, err
	}
	a.eventLog = el
	return a, nil
}

func (a *ACARSLog) insert(m ACARSMessage) error {
	at := m.ReceivedAt
	if at.IsZero() {
		at = time.Now()
	}
	_, err := a.db.SQL().Exec(
		`INSERT INTO acars_log
		 (received_at, mode, address, ack, label, block_id, downlink, msg_no,
		  flight_id, text, more, crc_ok, corrected, raw_hex, serial, frequency_hz)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		at.UnixNano(), m.Mode, m.Address, m.Ack, m.Label, m.BlockID, b2i(m.Downlink),
		m.MsgNo, m.FlightID, m.Text, b2i(m.More), b2i(m.CRCOK), m.Corrected,
		m.RawHex, m.Serial, int64(m.FrequencyHz),
	)
	return err
}

// Recent returns the most recent blocks, newest first. limit ≤ 0 picks 200;
// limit > 5000 caps at 5000.
func (a *ACARSLog) Recent(limit int) ([]ACARSMessage, error) {
	if limit <= 0 {
		limit = 200
	}
	if limit > 5000 {
		limit = 5000
	}
	rows, err := a.db.SQL().Query(
		`SELECT id, received_at, mode, address, ack, label, block_id, downlink, msg_no,
		        flight_id, text, more, crc_ok, corrected, raw_hex, serial, frequency_hz
		 FROM acars_log ORDER BY received_at DESC, id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("storage/acarslog: query: %w", err)
	}
	defer rows.Close()
	var out []ACARSMessage
	for rows.Next() {
		var (
			m                     ACARSMessage
			ns, freq              int64
			downlink, more, crcOK int
		)
		if err := rows.Scan(&m.ID, &ns, &m.Mode, &m.Address, &m.Ack, &m.Label, &m.BlockID,
			&downlink, &m.MsgNo, &m.FlightID, &m.Text, &more, &crcOK, &m.Corrected,
			&m.RawHex, &m.Serial, &freq); err != nil {
			return nil, fmt.Errorf("storage/acarslog: scan: %w", err)
		}
		m.ReceivedAt = time.Unix(0, ns)
		m.FrequencyHz = uint32(freq)
		m.Downlink, m.More, m.CRCOK = downlink != 0, more != 0, crcOK != 0
		out = append(out, m)
	}
	return out, rows.Err()
}
