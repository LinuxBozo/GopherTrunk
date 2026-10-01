package api

import (
	"net/http"
	"strconv"
	"time"

	"github.com/MattCheramie/GopherTrunk/internal/storage"
)

// ACARSProvider is the read surface the acars-log endpoint consumes
// (#1231). The daemon implements it on top of storage.ACARSLog; tests
// substitute a fake.
type ACARSProvider interface {
	RecentACARSMessages(limit int) ([]storage.ACARSMessage, error)
}

// ACARSMessageDTO is the JSON wire shape for the acars-log endpoint.
// Optional text fields stay omitted when empty so the wire stays compact.
type ACARSMessageDTO struct {
	ID          int64     `json:"id"`
	ReceivedAt  time.Time `json:"received_at"`
	Mode        string    `json:"mode,omitempty"`
	Address     string    `json:"address"`
	Ack         string    `json:"ack,omitempty"`
	Label       string    `json:"label"`
	BlockID     string    `json:"block_id,omitempty"`
	Downlink    bool      `json:"downlink"`
	MsgNo       string    `json:"msg_no,omitempty"`
	FlightID    string    `json:"flight_id,omitempty"`
	Text        string    `json:"text,omitempty"`
	More        bool      `json:"more,omitempty"`
	CRCOK       bool      `json:"crc_ok"`
	Corrected   int       `json:"corrected,omitempty"`
	RawHex      string    `json:"raw_hex,omitempty"`
	Serial      string    `json:"serial,omitempty"`
	FrequencyHz uint32    `json:"frequency_hz,omitempty"`
}

func acarsMessageToDTO(m storage.ACARSMessage) ACARSMessageDTO {
	return ACARSMessageDTO{
		ID: m.ID, ReceivedAt: m.ReceivedAt, Mode: m.Mode, Address: m.Address,
		Ack: m.Ack, Label: m.Label, BlockID: m.BlockID, Downlink: m.Downlink,
		MsgNo: m.MsgNo, FlightID: m.FlightID, Text: m.Text, More: m.More,
		CRCOK: m.CRCOK, Corrected: m.Corrected, RawHex: m.RawHex,
		Serial: m.Serial, FrequencyHz: m.FrequencyHz,
	}
}

// handleACARSMessages answers GET /api/v1/acars/messages. Optional ?limit=
// (default 200, max 5000). 503 when the storage layer isn't wired (daemon
// started without storage.path).
func (s *Server) handleACARSMessages(w http.ResponseWriter, r *http.Request) {
	if s.acars == nil {
		s.writeError(w, http.StatusServiceUnavailable, "acars subsystem not enabled (set storage.path in config to persist and view decoded messages)")
		return
	}
	limit := 200
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			limit = n
		}
	}
	rows, err := s.acars.RecentACARSMessages(limit)
	if err != nil {
		s.log.Error("api: acars messages", "err", err)
		s.writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	out := make([]ACARSMessageDTO, 0, len(rows))
	for _, m := range rows {
		out = append(out, acarsMessageToDTO(m))
	}
	writeJSON(w, http.StatusOK, out)
}
