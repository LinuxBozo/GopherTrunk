package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/MattCheramie/GopherTrunk/internal/alerts"
	"github.com/MattCheramie/GopherTrunk/internal/events"
)

type fakeAlerts struct {
	tested []string
	fail   bool
}

func (f *fakeAlerts) Status() alerts.Status {
	return alerts.Status{
		Channels: []alerts.ChannelStatus{{Name: "phone", Type: "ntfy", Sent: 3}},
		Rules:    []alerts.RuleStatus{{Name: "fire", Channels: []string{"phone"}, Fired: 3}},
		Matched:  3,
		Recent:   []alerts.Firing{{Rule: "fire", Title: "Call", Text: "Metro · TG 1001"}},
	}
}

func (f *fakeAlerts) Test(_ context.Context, channel string) error {
	f.tested = append(f.tested, channel)
	if f.fail || channel == "nope" {
		return errors.New("no channel \"" + channel + "\"")
	}
	return nil
}

// TestAlertsEndpoints: GET /api/v1/alerts reports configured:false when
// unwired and the manager's snapshot otherwise; POST /alerts/test/{channel}
// delivers a test notification and surfaces delivery errors as 502.
func TestAlertsEndpoints(t *testing.T) {
	bus := events.NewBus(8)
	defer bus.Close()
	base, teardown := mkServer(t, ServerOptions{Bus: bus})
	resp := mustGet(t, base+"/api/v1/alerts")
	var body map[string]any
	json.NewDecoder(resp.Body).Decode(&body)
	resp.Body.Close()
	teardown()
	if body["configured"] != false {
		t.Fatalf("unwired: %v", body)
	}

	fa := &fakeAlerts{}
	base, teardown = mkServer(t, ServerOptions{Bus: bus, Alerts: fa, AllowMutations: true})
	defer teardown()
	resp = mustGet(t, base+"/api/v1/alerts")
	json.NewDecoder(resp.Body).Decode(&body)
	resp.Body.Close()
	if body["configured"] != true || body["matched"].(float64) != 3 {
		t.Fatalf("status: %v", body)
	}
	chans := body["channels"].([]any)
	if len(chans) != 1 || chans[0].(map[string]any)["name"] != "phone" {
		t.Fatalf("channels: %v", chans)
	}
	r, err := http.Post(base+"/api/v1/alerts/test/phone", "application/json", strings.NewReader(""))
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	if r.StatusCode != 200 || len(fa.tested) != 1 || fa.tested[0] != "phone" {
		t.Fatalf("test phone: status=%d tested=%v", r.StatusCode, fa.tested)
	}
	r, _ = http.Post(base+"/api/v1/alerts/test/nope", "application/json", strings.NewReader(""))
	var e map[string]any
	json.NewDecoder(r.Body).Decode(&e)
	r.Body.Close()
	if r.StatusCode != http.StatusBadGateway || !strings.Contains(e["error"].(string), "nope") {
		t.Fatalf("test nope: status=%d body=%v", r.StatusCode, e)
	}
}
