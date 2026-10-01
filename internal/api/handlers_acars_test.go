package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/MattCheramie/GopherTrunk/internal/events"
	"github.com/MattCheramie/GopherTrunk/internal/storage"
)

type fakeACARSProvider struct {
	msgs     []storage.ACARSMessage
	gotLimit int
}

func (f *fakeACARSProvider) RecentACARSMessages(limit int) ([]storage.ACARSMessage, error) {
	f.gotLimit = limit
	return f.msgs, nil
}

func newACARSTestServer(t *testing.T, prov ACARSProvider) *httptest.Server {
	t.Helper()
	bus := events.NewBus(8)
	t.Cleanup(bus.Close)
	srv, err := NewServer(ServerOptions{Addr: "127.0.0.1:0", Bus: bus, ACARS: prov})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	ts := httptest.NewServer(srv.routes())
	t.Cleanup(ts.Close)
	return ts
}

func TestACARSMessagesReturns503WhenNotWired(t *testing.T) {
	ts := newACARSTestServer(t, nil)
	resp, err := http.Get(ts.URL + "/api/v1/acars/messages")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", resp.StatusCode)
	}
}

func TestACARSMessagesReturnsList(t *testing.T) {
	prov := &fakeACARSProvider{msgs: []storage.ACARSMessage{{
		ID: 7, ReceivedAt: time.Unix(1735000000, 0).UTC(), Mode: "2", Address: "G-DBCK",
		Ack: "W", Label: "_d", BlockID: "0", Downlink: true, MsgNo: "S64A", FlightID: "BA031T",
		CRCOK: true, Serial: "00000001", FrequencyHz: 131_550_000,
	}}}
	ts := newACARSTestServer(t, prov)
	resp, err := http.Get(ts.URL + "/api/v1/acars/messages?limit=5")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var out []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if prov.gotLimit != 5 || len(out) != 1 {
		t.Fatalf("limit=%d rows=%d", prov.gotLimit, len(out))
	}
	r := out[0]
	if r["address"] != "G-DBCK" || r["label"] != "_d" || r["flight_id"] != "BA031T" ||
		r["msg_no"] != "S64A" || r["downlink"] != true || r["crc_ok"] != true ||
		r["frequency_hz"] != float64(131_550_000) {
		t.Fatalf("row %v", r)
	}
	if _, ok := r["text"]; ok {
		t.Fatalf("empty text not omitted: %v", r)
	}
}
