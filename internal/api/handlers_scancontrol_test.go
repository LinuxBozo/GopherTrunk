package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/MattCheramie/GopherTrunk/internal/events"
	"github.com/MattCheramie/GopherTrunk/internal/trunking"
)

type fakeScanControl struct {
	hold   *trunking.HoldState
	avoids map[uint32]trunking.Avoid
}

func (f *fakeScanControl) Hold(system string, tg uint32) trunking.HoldState {
	h := trunking.HoldState{System: system, Talkgroup: tg, Since: time.Unix(1, 0)}
	f.hold = &h
	return h
}
func (f *fakeScanControl) ReleaseHold() bool { had := f.hold != nil; f.hold = nil; return had }
func (f *fakeScanControl) Held() (trunking.HoldState, bool) {
	if f.hold == nil {
		return trunking.HoldState{}, false
	}
	return *f.hold, true
}
func (f *fakeScanControl) AvoidTalkgroup(system string, tg uint32, d time.Duration) trunking.Avoid {
	if f.avoids == nil {
		f.avoids = map[uint32]trunking.Avoid{}
	}
	a := trunking.Avoid{System: system, Talkgroup: tg, Until: time.Unix(0, 0).Add(d)}
	f.avoids[tg] = a
	return a
}
func (f *fakeScanControl) UnavoidTalkgroup(_ string, tg uint32) bool {
	_, ok := f.avoids[tg]
	delete(f.avoids, tg)
	return ok
}
func (f *fakeScanControl) Avoids() []trunking.Avoid {
	out := []trunking.Avoid{}
	for _, a := range f.avoids {
		out = append(out, a)
	}
	return out
}

func doJSON(t *testing.T, method, url, body string) (int, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest(method, url, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

// TestScannerHoldAndAvoidEndpoints: hold / release and avoid / unavoid route
// to the engine, validate their bodies, default the avoid to 30 minutes, and
// 503 when the engine is not wired.
func TestScannerHoldAndAvoidEndpoints(t *testing.T) {
	bus := events.NewBus(8)
	defer bus.Close()
	base, teardown := mkServer(t, ServerOptions{Bus: bus, AllowMutations: true})
	if code, _ := doJSON(t, "POST", base+"/api/v1/scanner/hold", `{"talkgroup":5}`); code != 503 {
		t.Fatalf("unwired hold status = %d", code)
	}
	teardown()

	sc := &fakeScanControl{}
	base, teardown = mkServer(t, ServerOptions{Bus: bus, AllowMutations: true, ScanControl: sc})
	defer teardown()
	if code, _ := doJSON(t, "POST", base+"/api/v1/scanner/hold", `{"system":"Metro"}`); code != 400 {
		t.Fatalf("hold without talkgroup = %d", code)
	}
	code, body := doJSON(t, "POST", base+"/api/v1/scanner/hold", `{"system":"Metro","talkgroup":1001}`)
	if code != 200 || sc.hold == nil || sc.hold.Talkgroup != 1001 || body["hold"].(map[string]any)["system"] != "Metro" {
		t.Fatalf("hold: %d %v", code, body)
	}
	code, body = doJSON(t, "DELETE", base+"/api/v1/scanner/hold", "")
	if code != 200 || body["released"] != true || sc.hold != nil {
		t.Fatalf("release: %d %v", code, body)
	}

	code, body = doJSON(t, "POST", base+"/api/v1/talkgroups/77/avoid", "")
	if code != 200 {
		t.Fatalf("avoid default: %d %v", code, body)
	}
	if a := sc.avoids[77]; !a.Until.Equal(time.Unix(0, 0).Add(30 * time.Minute)) {
		t.Fatalf("default avoid until = %v (want 30m)", a.Until)
	}
	code, _ = doJSON(t, "POST", base+"/api/v1/talkgroups/78/avoid", `{"duration":"2h","system":"Metro"}`)
	if code != 200 || !sc.avoids[78].Until.Equal(time.Unix(0, 0).Add(2*time.Hour)) || sc.avoids[78].System != "Metro" {
		t.Fatalf("avoid 2h: %d %+v", code, sc.avoids[78])
	}
	code, _ = doJSON(t, "POST", base+"/api/v1/talkgroups/79/avoid", `{"minutes":5}`)
	if code != 200 || !sc.avoids[79].Until.Equal(time.Unix(0, 0).Add(5*time.Minute)) {
		t.Fatalf("avoid 5 min: %d %+v", code, sc.avoids[79])
	}
	if code, _ := doJSON(t, "POST", base+"/api/v1/talkgroups/80/avoid", `{"duration":"soon"}`); code != 400 {
		t.Fatalf("bad duration = %d", code)
	}
	if code, _ := doJSON(t, "POST", base+"/api/v1/talkgroups/x/avoid", ""); code != 400 {
		t.Fatalf("bad id = %d", code)
	}
	code, body = doJSON(t, "DELETE", base+"/api/v1/talkgroups/78/avoid", "")
	if code != 200 || body["cleared"] != true {
		t.Fatalf("unavoid: %d %v", code, body)
	}
	if _, still := sc.avoids[78]; still {
		t.Fatal("avoid 78 not cleared")
	}
}
