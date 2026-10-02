package alerts

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MattCheramie/GopherTrunk/internal/config"
	"github.com/MattCheramie/GopherTrunk/internal/events"
	"github.com/MattCheramie/GopherTrunk/internal/trunking"
	"github.com/MattCheramie/GopherTrunk/internal/voice/toneout"
)

func callStart(sys string, tg, src uint32, emerg, enc bool) events.Event {
	return events.Event{Kind: events.KindCallStart, Timestamp: time.Now(), Payload: trunking.CallStart{
		Grant:        trunking.Grant{System: sys, Protocol: "p25", GroupID: tg, SourceID: src, FrequencyHz: 851_012_500, Emergency: emerg, Encrypted: enc, AlgorithmID: algIf(enc)},
		Talkgroup:    &trunking.TalkGroup{ID: tg, AlphaTag: "FIRE DISP"},
		DeviceSerial: "VOICE-1",
	}}
}

func algIf(enc bool) uint8 {
	if enc {
		return 0x84
	}
	return 0
}

// TestRuleMatching covers every rule condition on the normalised event.
func TestRuleMatching(t *testing.T) {
	mk := func(c config.AlertRuleConfig) *Rule {
		c.Name = "r"
		c.Channels = []string{"x"}
		r, err := NewRule(c)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	norm := func(ev events.Event) Event {
		e, ok := Normalize(ev)
		if !ok {
			t.Fatalf("event %s not normalised", ev.Kind)
		}
		return e
	}
	plain := norm(callStart("Metro", 1001, 7001, false, false))
	emerg := norm(callStart("Metro", 1002, 7002, true, false))
	enc := norm(callStart("County", 2001, 8001, false, true))

	cases := []struct {
		name string
		rule config.AlertRuleConfig
		ev   Event
		want bool
	}{
		{"default kind call.start", config.AlertRuleConfig{}, plain, true},
		{"other kind", config.AlertRuleConfig{On: []string{"call.end"}}, plain, false},
		{"system match (case-insensitive)", config.AlertRuleConfig{Systems: []string{"metro"}}, plain, true},
		{"system mismatch", config.AlertRuleConfig{Systems: []string{"County"}}, plain, false},
		{"talkgroup match", config.AlertRuleConfig{Talkgroups: []uint32{1001}}, plain, true},
		{"talkgroup mismatch", config.AlertRuleConfig{Talkgroups: []uint32{1002}}, plain, false},
		{"radio match", config.AlertRuleConfig{Radios: []uint32{7001}}, plain, true},
		{"radio mismatch", config.AlertRuleConfig{Radios: []uint32{1}}, plain, false},
		{"emergency only — plain", config.AlertRuleConfig{Emergency: true}, plain, false},
		{"emergency only — emergency", config.AlertRuleConfig{Emergency: true}, emerg, true},
		{"encrypted only — clear", config.AlertRuleConfig{Encrypted: "only"}, plain, false},
		{"encrypted only — encrypted", config.AlertRuleConfig{Encrypted: "only"}, enc, true},
		{"encrypted exclude — encrypted", config.AlertRuleConfig{Encrypted: "exclude"}, enc, false},
		{"encrypted exclude — clear", config.AlertRuleConfig{Encrypted: "exclude"}, plain, true},
	}
	for _, c := range cases {
		if got := mk(c.rule).Matches(c.ev); got != c.want {
			t.Errorf("%s: Matches = %v, want %v", c.name, got, c.want)
		}
	}

	// Patched supergroup: a rule listing a member matches a call on the
	// supergroup.
	patched := norm(events.Event{Kind: events.KindCallStart, Payload: trunking.CallStart{
		Grant: trunking.Grant{System: "Metro", GroupID: 65000, PatchedGroups: []uint32{1001, 1003}},
	}})
	if !mk(config.AlertRuleConfig{Talkgroups: []uint32{1003}}).Matches(patched) {
		t.Error("patched member did not match")
	}
	// Tone profile filter.
	tone := norm(events.Event{Kind: events.KindToneAlert, Payload: toneout.Alert{Profile: "station-1", AlphaTag: "Station 1", System: "Metro", DeviceSerial: "V1", MatchedAt: time.Now(), FrequenciesHz: []float64{1042.2, 1297.4}}})
	if !mk(config.AlertRuleConfig{On: []string{"tone.alert"}, ToneProfiles: []string{"Station-1"}}).Matches(tone) {
		t.Error("tone profile did not match")
	}
	if mk(config.AlertRuleConfig{On: []string{"tone.alert"}, ToneProfiles: []string{"station-2"}}).Matches(tone) {
		t.Error("wrong tone profile matched")
	}
	// Minimum duration on call.end.
	short := norm(events.Event{Kind: events.KindCallEnd, Payload: trunking.CallEnd{Grant: trunking.Grant{System: "Metro", GroupID: 1}, StartedAt: time.Unix(0, 0), EndedAt: time.Unix(1, 0), Reason: trunking.EndReasonNormal}})
	if mk(config.AlertRuleConfig{On: []string{"call.end"}, MinDurationMs: 2000}).Matches(short) {
		t.Error("1 s call matched a 2 s minimum")
	}
	if !mk(config.AlertRuleConfig{On: []string{"call.end"}, MinDurationMs: 500}).Matches(short) {
		t.Error("1 s call did not match a 0.5 s minimum")
	}
}

// TestRuleCooldownAndMessages: the cooldown throttles per (system,
// talkgroup); the default message carries alias / source / flags; a custom
// template renders the event fields; a bad template fails at compile time.
func TestRuleCooldownAndMessages(t *testing.T) {
	r, err := NewRule(config.AlertRuleConfig{Name: "fire", Channels: []string{"c"}, Cooldown: "30s"})
	if err != nil {
		t.Fatal(err)
	}
	e1, _ := Normalize(callStart("Metro", 1001, 7001, false, false))
	e2, _ := Normalize(callStart("Metro", 1002, 7001, false, false))
	now := time.Unix(1000, 0)
	if !r.Fire(e1, now) {
		t.Fatal("first firing suppressed")
	}
	if r.Fire(e1, now.Add(10*time.Second)) {
		t.Fatal("repeat within cooldown fired")
	}
	if !r.Fire(e2, now.Add(10*time.Second)) {
		t.Fatal("a different talkgroup was throttled by another's cooldown")
	}
	if !r.Fire(e1, now.Add(31*time.Second)) {
		t.Fatal("firing after cooldown suppressed")
	}
	if f, c := r.Stats(); f != 3 || c != 1 {
		t.Fatalf("stats fired=%d cooled=%d", f, c)
	}

	n := r.Render(e1)
	for _, want := range []string{"Metro", "FIRE DISP (1001)", "from 7001", "851.0125 MHz"} {
		if !strings.Contains(n.Text, want) {
			t.Errorf("default message %q lacks %q", n.Text, want)
		}
	}
	if n.Title != "Call" {
		t.Errorf("title = %q", n.Title)
	}
	emerg, _ := Normalize(callStart("Metro", 1002, 7002, true, true))
	n = r.Render(emerg)
	if n.Title != "EMERGENCY call" || !strings.Contains(n.Text, "EMERGENCY") || !strings.Contains(n.Text, "encrypted (AES-256)") {
		t.Errorf("emergency/encrypted message: title=%q text=%q", n.Title, n.Text)
	}

	tr, err := NewRule(config.AlertRuleConfig{Name: "t", Channels: []string{"c"}, Message: "{{.System}}/{{.Talkgroup}} {{.TalkgroupAlpha}} {{.FrequencyMHz}} {{if .Emergency}}!!{{end}}"})
	if err != nil {
		t.Fatal(err)
	}
	if got := tr.Render(emerg).Text; got != "Metro/1002 FIRE DISP 851.0125 !!" {
		t.Errorf("template rendered %q", got)
	}
	if _, err := NewRule(config.AlertRuleConfig{Name: "bad", Channels: []string{"c"}, Message: "{{.System"}); err == nil {
		t.Error("bad template compiled")
	}
}

// TestNormalizeKinds: every watched payload maps onto the common fields.
func TestNormalizeKinds(t *testing.T) {
	at := time.Unix(1700000000, 0)
	cases := []struct {
		ev   events.Event
		kind string
		chk  func(Event) bool
	}{
		{events.Event{Kind: events.KindCallComplete, Payload: trunking.CallComplete{Grant: trunking.Grant{System: "S", GroupID: 5}, StartedAt: at, EndedAt: at.Add(4 * time.Second), AudioPath: "/tmp/x.wav"}}, "call.complete",
			func(e Event) bool {
				return e.AudioPath == "/tmp/x.wav" && e.Duration == 4*time.Second && e.DurationSeconds() == "4.0"
			}},
		{events.Event{Kind: events.KindGrant, Payload: trunking.Grant{System: "S", GroupID: 9, GroupLabel: "PD", At: at}}, "grant",
			func(e Event) bool { return e.TalkgroupAlpha == "PD" && e.At.Equal(at) }},
		{events.Event{Kind: events.KindAffiliation, Payload: trunking.Affiliation{System: "S", SourceID: 3, GroupID: 4}}, "affiliation",
			func(e Event) bool { return e.Source == 3 && e.Talkgroup == 4 }},
		{events.Event{Kind: events.KindUnitRegistration, Payload: trunking.UnitRegistration{System: "S", SourceID: 3}}, "registration",
			func(e Event) bool { return e.Source == 3 }},
		{events.Event{Kind: events.KindPatch, Payload: trunking.Patch{System: "S", SuperGroup: 65000, Members: []uint32{1, 2}, At: at}}, "patch",
			func(e Event) bool { return e.Talkgroup == 65000 && len(e.PatchedGroups) == 2 }},
		{events.Event{Kind: events.KindCallEncryption, Payload: trunking.CallEncryption{System: "S", GroupID: 1, AlgorithmID: 0xAA, KeyID: 2, At: at}}, "call.encryption",
			func(e Event) bool { return e.Encrypted && e.Algorithm() == "ADP/RC4" && e.KeyID == 2 }},
		{events.Event{Kind: events.KindTalkerAlias, Payload: trunking.TalkerAlias{System: "S", SourceID: 8, Alias: "ENG 1", At: at}}, "talker.alias",
			func(e Event) bool { return e.Alias == "ENG 1" && e.Source == 8 }},
		{events.Event{Kind: events.KindLocation, Payload: trunking.Location{System: "S", RadioID: 8, Latitude: 1.5, Longitude: -2.5, At: at}}, "location",
			func(e Event) bool { return e.Latitude == 1.5 && e.Longitude == -2.5 && e.Source == 8 }},
		{events.Event{Kind: events.KindCCLost, Payload: struct {
			System      string
			FrequencyHz uint32
		}{"S", 851_000_000}}, "cc.lost",
			func(e Event) bool { return e.System == "S" && e.FrequencyMHz() == "851.0000" }},
	}
	for _, c := range cases {
		e, ok := Normalize(c.ev)
		if !ok {
			t.Errorf("%s: not normalised", c.ev.Kind)
			continue
		}
		if e.Kind != c.kind || e.System != "S" {
			t.Errorf("%s: kind=%q system=%q", c.ev.Kind, e.Kind, e.System)
		}
		if !c.chk(e) {
			t.Errorf("%s: fields wrong: %+v", c.ev.Kind, e)
		}
		if defaultMessage(e) == "" {
			t.Errorf("%s: empty default message", c.ev.Kind)
		}
	}
	if _, ok := Normalize(events.Event{Kind: events.KindDecodeError, Payload: "x"}); ok {
		t.Error("decode.error normalised")
	}
}

// capture is an httptest handler that records every request.
type capture struct {
	mu   sync.Mutex
	reqs []capturedReq
}

type capturedReq struct {
	path, ct, auth string
	body           []byte
	headers        http.Header
}

func (c *capture) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		c.mu.Lock()
		c.reqs = append(c.reqs, capturedReq{path: r.URL.Path, ct: r.Header.Get("Content-Type"), auth: r.Header.Get("Authorization"), body: b, headers: r.Header.Clone()})
		c.mu.Unlock()
		w.WriteHeader(200)
	})
}

func (c *capture) last(t *testing.T) capturedReq {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.reqs) == 0 {
		t.Fatal("no request captured")
	}
	return c.reqs[len(c.reqs)-1]
}

// TestHTTPChannelsPostTheServicePayloads drives each HTTP channel against a
// recording server and checks the request shape each service documents.
func TestHTTPChannelsPostTheServicePayloads(t *testing.T) {
	cap := &capture{}
	srv := httptest.NewServer(cap.handler())
	defer srv.Close()
	e, _ := Normalize(callStart("Metro", 1001, 7001, true, false))
	n := Notification{Rule: "fire", Title: e.Title(), Text: defaultMessage(e), At: e.At, Event: e}
	ctx := context.Background()
	send := func(cfg config.AlertChannelConfig) capturedReq {
		cfg.Name = cfg.Type
		ch, err := NewChannel(cfg, srv.Client())
		if err != nil {
			t.Fatal(err)
		}
		if err := ch.Send(ctx, n); err != nil {
			t.Fatalf("%s: %v", cfg.Type, err)
		}
		return cap.last(t)
	}
	// Discord
	r := send(config.AlertChannelConfig{Type: "discord", URL: srv.URL + "/discord"})
	var dj map[string]any
	json.Unmarshal(r.body, &dj)
	if r.path != "/discord" || !strings.Contains(dj["content"].(string), "EMERGENCY") {
		t.Errorf("discord: %s %s", r.path, r.body)
	}
	// Slack
	r = send(config.AlertChannelConfig{Type: "slack", URL: srv.URL + "/slack"})
	json.Unmarshal(r.body, &dj)
	if !strings.Contains(dj["text"].(string), "Metro") {
		t.Errorf("slack: %s", r.body)
	}
	// ntfy: body is the text, Title/Priority/Tags headers, bearer token.
	r = send(config.AlertChannelConfig{Type: "ntfy", URL: srv.URL + "/topic", Token: "tk_abc", Priority: 4})
	if !strings.Contains(string(r.body), "TG FIRE DISP") || r.headers.Get("Title") != "EMERGENCY call" || r.headers.Get("Priority") != "4" || r.auth != "Bearer tk_abc" || r.headers.Get("Tags") != "rotating_light" {
		t.Errorf("ntfy: body=%q headers=%v", r.body, r.headers)
	}
	// Pushover: form fields.
	r = send(config.AlertChannelConfig{Type: "pushover", URL: srv.URL + "/pushover", Token: "app", User: "usr", Priority: 1})
	if !strings.Contains(r.ct, "x-www-form-urlencoded") || !strings.Contains(string(r.body), "token=app") || !strings.Contains(string(r.body), "user=usr") || !strings.Contains(string(r.body), "priority=1") {
		t.Errorf("pushover: ct=%s body=%s", r.ct, r.body)
	}
	// Telegram sendMessage with chat_id and MarkdownV2 escaping.
	r = send(config.AlertChannelConfig{Type: "telegram", URL: srv.URL, Token: "123:abc", User: "42"})
	json.Unmarshal(r.body, &dj)
	if r.path != "/bot123:abc/sendMessage" || dj["chat_id"] != "42" || !strings.Contains(dj["text"].(string), `851\.0125`) {
		t.Errorf("telegram: %s %s", r.path, r.body)
	}
	// Gotify: /message with X-Gotify-Key.
	r = send(config.AlertChannelConfig{Type: "gotify", URL: srv.URL + "/gotify/", Token: "gk", Priority: 7})
	json.Unmarshal(r.body, &dj)
	if r.path != "/gotify/message" || r.headers.Get("X-Gotify-Key") != "gk" || dj["priority"].(float64) != 7 {
		t.Errorf("gotify: %s %v %s", r.path, r.headers, r.body)
	}
	// Generic webhook: the whole Notification as JSON + bearer.
	r = send(config.AlertChannelConfig{Type: "webhook", URL: srv.URL + "/hook", Token: "wh"})
	var wn Notification
	if err := json.Unmarshal(r.body, &wn); err != nil || wn.Rule != "fire" || wn.Event.Talkgroup != 1001 || r.auth != "Bearer wh" {
		t.Errorf("webhook: %v %s auth=%s", err, r.body, r.auth)
	}
	// A non-2xx is an error naming the status.
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "nope", 403) }))
	defer bad.Close()
	ch, _ := NewChannel(config.AlertChannelConfig{Name: "s", Type: "slack", URL: bad.URL}, bad.Client())
	if err := ch.Send(ctx, n); err == nil || !strings.Contains(err.Error(), "403") {
		t.Errorf("slack 403 → %v", err)
	}
}

// TestAudioAttachmentsGoMultipart: a call.complete notification with a
// recording is sent as multipart (Discord files[0] + payload_json, Telegram
// sendAudio, webhook audio part); a missing file falls back to text.
func TestAudioAttachmentsGoMultipart(t *testing.T) {
	cap := &capture{}
	srv := httptest.NewServer(cap.handler())
	defer srv.Close()
	wav := filepath.Join(t.TempDir(), "call.wav")
	os.WriteFile(wav, []byte("RIFF....WAVEfmt "), 0o644)
	e, _ := Normalize(events.Event{Kind: events.KindCallComplete, Payload: trunking.CallComplete{Grant: trunking.Grant{System: "S", GroupID: 1}, AudioPath: wav}})
	n := Notification{Rule: "rec", Title: "Recording", Text: "x", Event: e, AudioPath: wav}
	parts := func(r capturedReq) map[string]string {
		mt, params, err := mime.ParseMediaType(r.ct)
		if err != nil || mt != "multipart/form-data" {
			t.Fatalf("not multipart: %s", r.ct)
		}
		mr := multipart.NewReader(bytes.NewReader(r.body), params["boundary"])
		out := map[string]string{}
		for {
			p, err := mr.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			b, _ := io.ReadAll(p)
			key := p.FormName()
			if p.FileName() != "" {
				key += ":" + p.FileName()
			}
			out[key] = string(b)
		}
		return out
	}
	ctx := context.Background()
	d, _ := NewChannel(config.AlertChannelConfig{Name: "d", Type: "discord", URL: srv.URL + "/d"}, srv.Client())
	if err := d.Send(ctx, n); err != nil {
		t.Fatal(err)
	}
	p := parts(cap.last(t))
	if !strings.Contains(p["payload_json"], "Recording") || p["files[0]:call.wav"] == "" {
		t.Errorf("discord parts: %v", p)
	}
	tg, _ := NewChannel(config.AlertChannelConfig{Name: "t", Type: "telegram", URL: srv.URL, Token: "1:a", User: "9"}, srv.Client())
	if err := tg.Send(ctx, n); err != nil {
		t.Fatal(err)
	}
	r := cap.last(t)
	p = parts(r)
	if r.path != "/bot1:a/sendAudio" || p["chat_id"] != "9" || p["audio:call.wav"] == "" {
		t.Errorf("telegram parts: %s %v", r.path, p)
	}
	wh, _ := NewChannel(config.AlertChannelConfig{Name: "w", Type: "webhook", URL: srv.URL + "/w"}, srv.Client())
	if err := wh.Send(ctx, n); err != nil {
		t.Fatal(err)
	}
	p = parts(cap.last(t))
	if !strings.Contains(p["alert"], `"rule":"rec"`) || p["audio:call.wav"] == "" {
		t.Errorf("webhook parts: %v", p)
	}
	// Missing file: ensureAudio clears the attachment and text still goes.
	gone := n
	gone.AudioPath = filepath.Join(t.TempDir(), "missing.wav")
	ensureAudio(&gone)
	if gone.AudioPath != "" {
		t.Error("missing audio not cleared")
	}
}

// TestExecChannelRunsCommandWithJSONAndEnv: the command gets the JSON on
// stdin and the GT_ALERT_* environment.
func TestExecChannelRunsCommandWithJSONAndEnv(t *testing.T) {
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("no /bin/sh")
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "alert.sh")
	out := filepath.Join(dir, "out.txt")
	os.WriteFile(script, []byte("#!/bin/sh\ncat > \"$1\"\necho \"$GT_ALERT_SYSTEM|$GT_ALERT_TALKGROUP|$GT_ALERT_EMERGENCY|$GT_ALERT_RULE\" >> \"$1\"\n"), 0o755)
	ch, err := NewChannel(config.AlertChannelConfig{Name: "x", Type: "exec", Command: script + " " + out}, nil)
	if err != nil {
		t.Fatal(err)
	}
	e, _ := Normalize(callStart("Metro", 1001, 7001, true, false))
	n := Notification{Rule: "fire", Title: e.Title(), Text: "t", Event: e}
	if err := ch.Send(context.Background(), n); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(out)
	s := string(b)
	if !strings.Contains(s, `"rule":"fire"`) || !strings.HasSuffix(strings.TrimSpace(s), "Metro|1001|true|fire") {
		t.Errorf("exec output: %s", s)
	}
	// A failing command surfaces its exit status.
	fail, _ := NewChannel(config.AlertChannelConfig{Name: "f", Type: "exec", Command: "/bin/sh -c exit_3"}, nil)
	if err := fail.Send(context.Background(), n); err == nil {
		t.Error("failing command reported success")
	}
}

// fakeBroker speaks just enough MQTT 3.1.1 to accept CONNECT and collect
// PUBLISH packets.
type fakeBroker struct {
	ln         net.Listener
	mu         sync.Mutex
	published  map[string][]byte
	connects   int
	user, pass string
}

func newFakeBroker(t *testing.T) *fakeBroker {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	b := &fakeBroker{ln: ln, published: map[string][]byte{}}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go b.serve(c)
		}
	}()
	return b
}

func readPacket(c net.Conn) (byte, []byte, error) {
	var h [1]byte
	if _, err := io.ReadFull(c, h[:]); err != nil {
		return 0, nil, err
	}
	n, mult := 0, 1
	for {
		var d [1]byte
		if _, err := io.ReadFull(c, d[:]); err != nil {
			return 0, nil, err
		}
		n += int(d[0]&0x7F) * mult
		if d[0]&0x80 == 0 {
			break
		}
		mult *= 128
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(c, body); err != nil {
		return 0, nil, err
	}
	return h[0], body, nil
}

func (b *fakeBroker) serve(c net.Conn) {
	defer c.Close()
	typ, body, err := readPacket(c)
	if err != nil || typ>>4 != 1 {
		return
	}
	// CONNECT: "MQTT" level flags keepalive clientID [user] [pass]
	b.mu.Lock()
	b.connects++
	b.mu.Unlock()
	flags := body[7]
	off := 10
	cl := int(body[off])<<8 | int(body[off+1])
	off += 2 + cl
	var user, pass string
	if flags&0x80 != 0 {
		ul := int(body[off])<<8 | int(body[off+1])
		user = string(body[off+2 : off+2+ul])
		off += 2 + ul
	}
	if flags&0x40 != 0 {
		pl := int(body[off])<<8 | int(body[off+1])
		pass = string(body[off+2 : off+2+pl])
	}
	b.mu.Lock()
	b.user, b.pass = user, pass
	b.mu.Unlock()
	code := byte(0)
	if b.user != "" && b.pass != "secret" {
		code = 4
	}
	c.Write([]byte{0x20, 0x02, 0x00, code})
	if code != 0 {
		return
	}
	for {
		typ, body, err := readPacket(c)
		if err != nil {
			return
		}
		switch typ >> 4 {
		case 3: // PUBLISH QoS 0
			tl := int(body[0])<<8 | int(body[1])
			topic := string(body[2 : 2+tl])
			b.mu.Lock()
			b.published[topic] = append([]byte(nil), body[2+tl:]...)
			b.mu.Unlock()
		case 12: // PINGREQ
			c.Write([]byte{0xD0, 0x00})
		case 14: // DISCONNECT
			return
		}
	}
}

// TestMQTTChannelPublishesAlertsAndMirrorsEvents: CONNECT with credentials,
// alert PUBLISH on <topic>/alerts/<rule>, event mirror on
// <topic>/events/<kind>, and a bad password is reported.
func TestMQTTChannelPublishesAlertsAndMirrorsEvents(t *testing.T) {
	br := newFakeBroker(t)
	defer br.ln.Close()
	ch, err := NewChannel(config.AlertChannelConfig{Name: "mq", Type: "mqtt", URL: "tcp://" + br.ln.Addr().String(), User: "gt", Password: "secret", Topic: "scanner", MirrorEvents: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	mc := ch.(*mqttChannel)
	n := Notification{Rule: "fire dispatch", Title: "Call", Text: "x", Event: Event{Kind: "call.start", System: "Metro", Talkgroup: 1001}}
	if err := mc.Send(context.Background(), n); err != nil {
		t.Fatal(err)
	}
	if err := mc.PublishEvent(context.Background(), "call.start", []byte(`{"kind":"call.start"}`)); err != nil {
		t.Fatal(err)
	}
	// Second publish reuses the connection.
	if err := mc.Send(context.Background(), n); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	br.mu.Lock()
	defer br.mu.Unlock()
	if br.connects != 1 || br.user != "gt" || br.pass != "secret" {
		t.Errorf("connects=%d user=%q pass=%q", br.connects, br.user, br.pass)
	}
	var got Notification
	if err := json.Unmarshal(br.published["scanner/alerts/fire_dispatch"], &got); err != nil || got.Rule != "fire dispatch" || got.Event.Talkgroup != 1001 {
		t.Errorf("alert publish: %v %s", err, br.published["scanner/alerts/fire_dispatch"])
	}
	if string(br.published["scanner/events/call.start"]) != `{"kind":"call.start"}` {
		t.Errorf("event mirror: %q", br.published["scanner/events/call.start"])
	}
	br.mu.Unlock()
	bad, _ := NewChannel(config.AlertChannelConfig{Name: "b", Type: "mqtt", URL: "tcp://" + br.ln.Addr().String(), User: "gt", Password: "wrong"}, nil)
	err = bad.Send(context.Background(), n)
	br.mu.Lock()
	if err == nil || !strings.Contains(err.Error(), "bad user name or password") {
		t.Errorf("bad password → %v", err)
	}
	// Packet encoders against the spec's literal bytes.
	if got := mqttEncodeRemaining(321); !bytes.Equal(got, []byte{0xC1, 0x02}) {
		t.Errorf("remaining length 321 = %x", got)
	}
	pkt := mqttPublishPacket("a/b", []byte("hi"))
	if !bytes.Equal(pkt, []byte{0x30, 0x07, 0x00, 0x03, 'a', '/', 'b', 'h', 'i'}) {
		t.Errorf("PUBLISH = %x", pkt)
	}
	cp := mqttConnectPacket("id", "", "", 60)
	if !bytes.Equal(cp[:12], []byte{0x10, 0x0E, 0x00, 0x04, 'M', 'Q', 'T', 'T', 0x04, 0x02, 0x00, 0x3C}) {
		t.Errorf("CONNECT = %x", cp)
	}
}

// TestManagerEndToEnd: bus event → matching rule → channel delivery, with a
// non-matching rule staying quiet, the status snapshot counting it all, and
// Test() reaching a channel directly.
func TestManagerEndToEnd(t *testing.T) {
	cap := &capture{}
	srv := httptest.NewServer(cap.handler())
	defer srv.Close()
	bus := events.NewBus(16)
	defer bus.Close()
	m, err := NewManager(Options{
		Bus: bus, HTTP: srv.Client(),
		Config: config.AlertsConfig{
			Channels: []config.AlertChannelConfig{
				{Name: "chat", Type: "slack", URL: srv.URL + "/slack"},
				{Name: "hook", Type: "webhook", URL: srv.URL + "/hook"},
			},
			Rules: []config.AlertRuleConfig{
				{Name: "fire", Talkgroups: []uint32{1001}, Channels: []string{"chat", "hook"}},
				{Name: "quiet", Talkgroups: []uint32{9}, Channels: []string{"chat"}},
				{Name: "off", Disabled: true, Channels: []string{"chat"}},
				{Name: "tones", On: []string{"tone.alert"}, Channels: []string{"chat"}},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if m.Rules() != 3 || m.Channels() != 2 {
		t.Fatalf("rules=%d channels=%d", m.Rules(), m.Channels())
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go m.Run(ctx)
	bus.Publish(callStart("Metro", 1001, 7001, false, false))
	bus.Publish(callStart("Metro", 1002, 7001, false, false)) // matches nothing
	bus.Publish(events.Event{Kind: events.KindToneAlert, Payload: toneout.Alert{Profile: "st1", System: "Metro", DeviceSerial: "V1", MatchedAt: time.Now()}})
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		cap.mu.Lock()
		n := len(cap.reqs)
		cap.mu.Unlock()
		if n >= 3 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	cap.mu.Lock()
	paths := map[string]int{}
	for _, r := range cap.reqs {
		paths[r.path]++
	}
	cap.mu.Unlock()
	if paths["/slack"] != 2 || paths["/hook"] != 1 {
		t.Fatalf("deliveries by path: %v", paths)
	}
	st := m.Status()
	if st.Matched != 2 || len(st.Recent) != 2 || st.Recent[0].Rule != "fire" || st.Recent[1].Rule != "tones" {
		t.Errorf("status: %+v", st)
	}
	var sent int
	for _, c := range st.Channels {
		sent += c.Sent
	}
	if sent != 3 {
		t.Errorf("sent total = %d", sent)
	}
	if err := m.Test(ctx, "chat"); err != nil {
		t.Fatal(err)
	}
	if err := m.Test(ctx, "nope"); err == nil {
		t.Error("unknown channel test succeeded")
	}
	// Unknown channel in a rule fails construction.
	if _, err := NewManager(Options{Bus: bus, Config: config.AlertsConfig{Rules: []config.AlertRuleConfig{{Name: "x", Channels: []string{"ghost"}}}}}); err == nil {
		t.Error("rule naming an undefined channel compiled")
	}
	m.Close()
}

// TestConfigValidation pins the alerts section's validator messages.
func TestConfigValidation(t *testing.T) {
	ok := config.Config{Alerts: config.AlertsConfig{
		Channels: []config.AlertChannelConfig{{Name: "d", Type: "discord", URL: "https://x"}, {Name: "p", Type: "pushover", Token: "t", User: "u"}},
		Rules:    []config.AlertRuleConfig{{Name: "r", Channels: []string{"d"}, Cooldown: "30s", Encrypted: "only"}},
	}}
	if errs := ok.ValidateSection("alerts"); len(errs) != 0 {
		t.Fatalf("valid config rejected: %v", errs)
	}
	bad := config.Config{Alerts: config.AlertsConfig{
		Channels: []config.AlertChannelConfig{{Name: "d", Type: "discord"}, {Name: "p", Type: "pushover"}, {Name: "m", Type: "mqtt", URL: "http://x"}, {Name: "z", Type: "pager"}},
		Rules: []config.AlertRuleConfig{
			{Name: "r", Channels: []string{"ghost"}, On: []string{"call.start", "bogus"}, Encrypted: "maybe", Cooldown: "soon", AttachAudio: true},
			{Channels: nil},
		},
	}}
	errs := bad.ValidateSection("alerts")
	joined := ""
	for _, e := range errs {
		joined += e.Error() + "\n"
	}
	for _, want := range []string{"url required", "pushover needs token", "mqtt url must be", `type "pager"`, `channel "ghost" is not defined`, `"bogus" must be one of`, "encrypted must be", `cooldown "soon"`, "attach_audio needs on: [call.complete]", "name required", "channels required"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing validation error %q in:\n%s", want, joined)
		}
	}
}
