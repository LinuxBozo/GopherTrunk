package api

import (
	"strings"
	"encoding/csv"
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/MattCheramie/GopherTrunk/internal/events"
	"github.com/MattCheramie/GopherTrunk/internal/storage"
	"github.com/MattCheramie/GopherTrunk/internal/trunking"
)

func TestCallHistoryEndpointSurfacesPersistedRows(t *testing.T) {
	bus := events.NewBus(8)
	defer bus.Close()
	dbPath := filepath.Join(t.TempDir(), "calls.db")
	db, err := storage.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	cl, err := storage.NewCallLog(db, bus, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go cl.Run(ctx)

	startedAt := time.Now().UTC().Truncate(time.Microsecond)
	bus.Publish(events.Event{
		Kind: events.KindCallStart,
		Payload: trunking.CallStart{
			Grant: trunking.Grant{
				System: "Alpha", Protocol: "p25",
				GroupID: 7777, FrequencyHz: 851_000_000,
			},
			Talkgroup:    &trunking.TalkGroup{ID: 7777, AlphaTag: "FIRE-DISP"},
			DeviceSerial: "VOICE-1",
			StartedAt:    startedAt,
		},
	})

	// Wait for the row.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		rows, _ := db.History(context.Background(), storage.HistoryFilter{Limit: 1})
		if len(rows) == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	base, teardown := mkServer(t, ServerOptions{
		Bus:     bus,
		History: HistoryFromStorage(db),
	})
	defer teardown()

	resp := mustGet(t, base+"/api/v1/calls/history")
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var body struct {
		Calls []CallRow `json:"calls"`
	}
	json.NewDecoder(resp.Body).Decode(&body)
	if len(body.Calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(body.Calls))
	}
	got := body.Calls[0]
	if got.System != "Alpha" || got.GroupID != 7777 || got.TalkgroupAlpha != "FIRE-DISP" {
		t.Errorf("row = %+v", got)
	}
}

func TestCallHistoryEndpointFiltersBySystem(t *testing.T) {
	bus := events.NewBus(8)
	defer bus.Close()
	db, _ := storage.Open(":memory:")
	defer db.Close()
	cl, _ := storage.NewCallLog(db, bus, nil)
	defer cl.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go cl.Run(ctx)

	startedAt := time.Now().UTC().Truncate(time.Microsecond)
	for i, sys := range []string{"Alpha", "Bravo", "Alpha"} {
		bus.Publish(events.Event{
			Kind: events.KindCallStart,
			Payload: trunking.CallStart{
				Grant:        trunking.Grant{System: sys, GroupID: uint32(100 + i), FrequencyHz: 1},
				DeviceSerial: "X" + sys,
				StartedAt:    startedAt.Add(time.Duration(i) * time.Second),
			},
		})
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		rows, _ := db.History(context.Background(), storage.HistoryFilter{Limit: 10})
		if len(rows) == 3 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	base, teardown := mkServer(t, ServerOptions{
		Bus:     bus,
		History: HistoryFromStorage(db),
	})
	defer teardown()

	resp := mustGet(t, base+"/api/v1/calls/history?system=Alpha")
	defer resp.Body.Close()
	var body struct {
		Calls []CallRow `json:"calls"`
	}
	json.NewDecoder(resp.Body).Decode(&body)
	if len(body.Calls) != 2 {
		t.Errorf("Alpha-filtered rows = %d, want 2", len(body.Calls))
	}
}

// The talkgroup filter is what the History page's GROUP ID box sends, and
// what a "view calls on this talkgroup" cross-link seeds. Only the rejection
// of a malformed group_id was covered before, so a filter that silently
// matched nothing would not have failed anything.
func TestCallHistoryEndpointFiltersByGroupID(t *testing.T) {
	bus := events.NewBus(8)
	defer bus.Close()
	db, _ := storage.Open(":memory:")
	defer db.Close()
	cl, _ := storage.NewCallLog(db, bus, nil)
	defer cl.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go cl.Run(ctx)

	startedAt := time.Now().UTC().Truncate(time.Microsecond)
	for i, tg := range []uint32{1020545, 900, 1020545} {
		bus.Publish(events.Event{
			Kind: events.KindCallStart,
			Payload: trunking.CallStart{
				Grant:        trunking.Grant{System: "250_013", GroupID: tg, FrequencyHz: 467_912_500},
				DeviceSerial: "TETRA-" + string(rune('A'+i)),
				StartedAt:    startedAt.Add(time.Duration(i) * time.Second),
			},
		})
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		rows, _ := db.History(context.Background(), storage.HistoryFilter{Limit: 10})
		if len(rows) == 3 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	base, teardown := mkServer(t, ServerOptions{
		Bus:     bus,
		History: HistoryFromStorage(db),
	})
	defer teardown()

	resp := mustGet(t, base+"/api/v1/calls/history?system=250_013&group_id=1020545")
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var body struct {
		Calls []CallRow `json:"calls"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if len(body.Calls) != 2 {
		t.Fatalf("group-filtered rows = %d, want 2", len(body.Calls))
	}
	for _, r := range body.Calls {
		if r.GroupID != 1020545 {
			t.Errorf("row group_id = %d, want 1020545", r.GroupID)
		}
	}
}

// call_log carries the encryption identifiers and the resolved source-radio
// alias, but the api DTO used to drop all three — so the History table's
// Source column and its "enc <alg> key <id>" badge could never populate no
// matter what the decoder wrote.
func TestCallHistoryEndpointCarriesEncryptionAndSourceAlias(t *testing.T) {
	bus := events.NewBus(8)
	defer bus.Close()
	db, _ := storage.Open(":memory:")
	defer db.Close()
	cl, _ := storage.NewCallLog(db, bus, nil)
	defer cl.Close()
	cl.SetRIDResolver(func(id uint32) string {
		if id == 1005736 {
			return "CPL-SMITH"
		}
		return ""
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go cl.Run(ctx)

	bus.Publish(events.Event{
		Kind: events.KindCallStart,
		Payload: trunking.CallStart{
			Grant: trunking.Grant{
				System: "Alpha", Protocol: "p25", GroupID: 4242,
				SourceID: 1005736, FrequencyHz: 851_000_000,
				Encrypted: true, AlgorithmID: 0x84, KeyID: 0x1234,
			},
			DeviceSerial: "VOICE-1",
			StartedAt:    time.Now().UTC().Truncate(time.Microsecond),
		},
	})
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		rows, _ := db.History(context.Background(), storage.HistoryFilter{Limit: 1})
		if len(rows) == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	base, teardown := mkServer(t, ServerOptions{
		Bus:     bus,
		History: HistoryFromStorage(db),
	})
	defer teardown()

	resp := mustGet(t, base+"/api/v1/calls/history")
	defer resp.Body.Close()
	var body struct {
		Calls []CallRow `json:"calls"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if len(body.Calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(body.Calls))
	}
	got := body.Calls[0]
	if got.AlgorithmID != 0x84 {
		t.Errorf("algorithm_id = %#x, want 0x84", got.AlgorithmID)
	}
	if got.KeyID != 0x1234 {
		t.Errorf("key_id = %#x, want 0x1234", got.KeyID)
	}
	if got.SourceAlpha != "CPL-SMITH" {
		t.Errorf("source_alpha = %q, want CPL-SMITH", got.SourceAlpha)
	}
}

func TestCallHistoryEndpointReturns503WithoutHistory(t *testing.T) {
	bus := events.NewBus(4)
	defer bus.Close()
	base, teardown := mkServer(t, ServerOptions{Bus: bus})
	defer teardown()
	resp := mustGet(t, base+"/api/v1/calls/history")
	defer resp.Body.Close()
	if resp.StatusCode != 503 {
		t.Errorf("status = %d, want 503", resp.StatusCode)
	}
}

func TestCallHistoryEndpointRejectsBadParams(t *testing.T) {
	bus := events.NewBus(4)
	defer bus.Close()
	db, _ := storage.Open(":memory:")
	defer db.Close()
	base, teardown := mkServer(t, ServerOptions{
		Bus:     bus,
		History: HistoryFromStorage(db),
	})
	defer teardown()

	for _, q := range []string{
		"group_id=abc",
		"source_id=abc",
		"since=not-a-date",
		"until=not-a-date",
		"limit=-1",
	} {
		resp := mustGet(t, base+"/api/v1/calls/history?"+q)
		if resp.StatusCode != 400 {
			t.Errorf("%s status = %d, want 400", q, resp.StatusCode)
		}
		resp.Body.Close()
	}
}

// TestCallHistoryEndpointFiltersBySourceID: ?source_id= narrows the call log
// to calls FROM one radio — the per-RID view (Radio IDs → "All calls from this
// radio"), so recordings can be found by who was talking, not only by
// talkgroup. The parameter used to be silently ignored (HistoryFilter.SourceID
// existed but the handler never parsed it).
func TestCallHistoryEndpointFiltersBySourceID(t *testing.T) {
	bus := events.NewBus(8)
	defer bus.Close()
	db, _ := storage.Open(":memory:")
	defer db.Close()
	cl, _ := storage.NewCallLog(db, bus, nil)
	defer cl.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go cl.Run(ctx)

	startedAt := time.Now().UTC().Truncate(time.Microsecond)
	for i, src := range []uint32{1005492, 1005546, 1005492} {
		bus.Publish(events.Event{
			Kind: events.KindCallStart,
			Payload: trunking.CallStart{
				Grant:        trunking.Grant{System: "250_013", GroupID: 1020543, SourceID: src, FrequencyHz: 467_912_500},
				DeviceSerial: "TETRA-" + string(rune('A'+i)),
				StartedAt:    startedAt.Add(time.Duration(i) * time.Second),
			},
		})
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		rows, _ := db.History(context.Background(), storage.HistoryFilter{Limit: 10})
		if len(rows) == 3 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	base, teardown := mkServer(t, ServerOptions{Bus: bus, History: HistoryFromStorage(db)})
	defer teardown()

	resp := mustGet(t, base+"/api/v1/calls/history?source_id=1005492")
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var body struct {
		Calls []CallRow `json:"calls"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if len(body.Calls) != 2 {
		t.Fatalf("source-filtered rows = %d, want 2 (source_id is ignored)", len(body.Calls))
	}
	for _, r := range body.Calls {
		if r.SourceID != 1005492 {
			t.Errorf("row source_id = %d, want 1005492", r.SourceID)
		}
	}
}

// TestCallHistoryEndpointSearchAndCSVExport: ?q= searches aliases and
// ?format=csv renders the same rows as a CSV download with the stable header.
func TestCallHistoryEndpointSearchAndCSVExport(t *testing.T) {
	bus := events.NewBus(8)
	defer bus.Close()
	dbPath := filepath.Join(t.TempDir(), "calls.db")
	db, err := storage.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cl, err := storage.NewCallLog(db, bus, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go cl.Run(ctx)

	startedAt := time.Now().UTC().Truncate(time.Microsecond)
	for i, tg := range []struct {
		id    uint32
		alpha string
		enc   bool
	}{{7777, "FIRE-DISP", false}, {8888, "PD-TAC", true}} {
		bus.Publish(events.Event{
			Kind: events.KindCallStart,
			Payload: trunking.CallStart{
				Grant: trunking.Grant{
					System: "Alpha", Protocol: "p25",
					GroupID: tg.id, FrequencyHz: 851_000_000, Encrypted: tg.enc,
					AlgorithmID: algIf(tg.enc, 0x84), KeyID: uint16(algIf(tg.enc, 3)),
				},
				Talkgroup:    &trunking.TalkGroup{ID: tg.id, AlphaTag: tg.alpha},
				DeviceSerial: "VOICE-" + string(rune('1'+i)),
				StartedAt:    startedAt.Add(time.Duration(i) * time.Second),
			},
		})
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		rows, _ := db.History(context.Background(), storage.HistoryFilter{Limit: 10})
		if len(rows) == 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	base, teardown := mkServer(t, ServerOptions{Bus: bus, History: HistoryFromStorage(db)})
	defer teardown()

	resp := mustGet(t, base+"/api/v1/calls/history?q=fire")
	var body struct {
		Calls []CallRow `json:"calls"`
	}
	json.NewDecoder(resp.Body).Decode(&body)
	resp.Body.Close()
	if len(body.Calls) != 1 || body.Calls[0].GroupID != 7777 {
		t.Fatalf("q=fire → %+v", body.Calls)
	}
	resp = mustGet(t, base+"/api/v1/calls/history?encrypted=true")
	json.NewDecoder(resp.Body).Decode(&body)
	resp.Body.Close()
	if len(body.Calls) != 1 || body.Calls[0].GroupID != 8888 {
		t.Fatalf("encrypted=true → %+v", body.Calls)
	}
	resp = mustGet(t, base+"/api/v1/calls/history?encrypted=maybe")
	resp.Body.Close()
	if resp.StatusCode != 400 {
		t.Fatalf("encrypted=maybe status = %d, want 400", resp.StatusCode)
	}

	resp = mustGet(t, base+"/api/v1/calls/history?format=csv")
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/csv") {
		t.Fatalf("content-type = %q", ct)
	}
	if cd := resp.Header.Get("Content-Disposition"); !strings.Contains(cd, "attachment") {
		t.Fatalf("content-disposition = %q", cd)
	}
	recs, err := csv.NewReader(resp.Body).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 3 {
		t.Fatalf("csv rows = %d (incl. header), want 3", len(recs))
	}
	if recs[0][0] != "id" || recs[0][7] != "talkgroup_alpha" || recs[0][13] != "encrypted" || recs[0][14] != "algorithm_id" {
		t.Fatalf("header = %v", recs[0])
	}
	// newest first: the encrypted PD-TAC row leads.
	if recs[1][7] != "PD-TAC" || recs[1][13] != "1" || recs[1][14] != "0x84" || recs[1][15] != "3" {
		t.Fatalf("row 1 = %v", recs[1])
	}
	if recs[2][7] != "FIRE-DISP" || recs[2][13] != "0" || recs[2][14] != "" {
		t.Fatalf("row 2 = %v", recs[2])
	}
}

type fakePatches struct{ p []trunking.PatchGroup }

func (f fakePatches) Patches() []trunking.PatchGroup { return f.p }

// TestPatchesEndpoint: GET /api/v1/patches serves the engine's live patch
// table (sorted by system, supergroup) and an empty list when unwired.
func TestPatchesEndpoint(t *testing.T) {
	bus := events.NewBus(8)
	defer bus.Close()
	base, teardown := mkServer(t, ServerOptions{Bus: bus})
	resp := mustGet(t, base+"/api/v1/patches")
	var body struct {
		Patches []trunking.PatchGroup `json:"patches"`
	}
	json.NewDecoder(resp.Body).Decode(&body)
	resp.Body.Close()
	teardown()
	if body.Patches == nil || len(body.Patches) != 0 {
		t.Fatalf("unwired patches = %v", body.Patches)
	}
	base, teardown = mkServer(t, ServerOptions{Bus: bus, Patches: fakePatches{p: []trunking.PatchGroup{
		{System: "Metro", SuperGroup: 65000, Members: []uint32{101, 102}, Vendor: "motorola"},
		{System: "County", SuperGroup: 64000, Members: []uint32{7}, Vendor: "harris"},
	}}})
	defer teardown()
	resp = mustGet(t, base+"/api/v1/patches")
	defer resp.Body.Close()
	json.NewDecoder(resp.Body).Decode(&body)
	if len(body.Patches) != 2 || body.Patches[0].System != "County" || body.Patches[1].SuperGroup != 65000 || len(body.Patches[1].Members) != 2 {
		t.Fatalf("patches = %+v", body.Patches)
	}
}

func algIf(cond bool, v uint8) uint8 {
	if cond {
		return v
	}
	return 0
}
