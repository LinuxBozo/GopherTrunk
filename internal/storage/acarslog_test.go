package storage

import (
	"context"
	"testing"
	"time"

	"github.com/MattCheramie/GopherTrunk/internal/events"
)

func TestACARSLogRoundTripsABlock(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	bus := events.NewBus(8)
	log, err := NewACARSLog(db, bus, nil)
	if err != nil {
		t.Fatalf("NewACARSLog: %v", err)
	}
	t.Cleanup(func() {
		_ = log.Close()
		bus.Close()
		_ = db.Close()
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = log.Run(ctx) }()

	want := ACARSMessage{
		ReceivedAt: time.Unix(1735000000, 0), Mode: "2", Address: "G-DBCK", Ack: "W",
		Label: "_d", BlockID: "0", Downlink: true, MsgNo: "S64A", FlightID: "BA031T",
		Text: "POS N51 W001\r\n", More: true, CRCOK: true, Corrected: 1,
		RawHex: "32aec7", Serial: "00000001", FrequencyHz: 131_550_000,
	}
	bus.Publish(events.Event{Kind: events.KindACARSMessage, Payload: want})

	var got []ACARSMessage
	for deadline := time.Now().Add(time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if got, _ = log.Recent(10); len(got) == 1 {
			break
		}
	}
	if len(got) != 1 {
		t.Fatalf("Recent = %d rows, want 1", len(got))
	}
	r := got[0]
	r.ID = 0
	if !r.ReceivedAt.Equal(want.ReceivedAt) {
		t.Fatalf("ReceivedAt = %v", r.ReceivedAt)
	}
	r.ReceivedAt = want.ReceivedAt
	if r != want {
		t.Fatalf("round trip:\n got %+v\nwant %+v", r, want)
	}
}

func TestACARSLogIsSweptByRetention(t *testing.T) {
	found := false
	for _, tbl := range decoderLogTables {
		found = found || tbl == "acars_log"
	}
	if !found {
		t.Fatal("acars_log is not in the retention sweep (retention.log_days)")
	}
}
