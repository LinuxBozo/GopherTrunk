package transcribe

import (
	"context"
	"encoding/binary"
	"io"
	"mime"
	"mime/multipart"
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
)

type fakeStore struct {
	mu   sync.Mutex
	rows map[string]string
}

func (f *fakeStore) SetTranscript(_ context.Context, path, text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.rows == nil {
		f.rows = map[string]string{}
	}
	f.rows[path] = text
	return nil
}

// writeWAV8k writes an 8 kHz mono 16-bit WAV of n samples (a 1 kHz tone).
func writeWAV8k(t *testing.T, dir string, n int) string {
	t.Helper()
	samples := make([]int16, n)
	for i := range samples {
		if (i/4)%2 == 0 {
			samples[i] = 8000
		} else {
			samples[i] = -8000
		}
	}
	p := filepath.Join(dir, "call.wav")
	if err := os.WriteFile(p, wavBytes(samples, 8000), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

type seenReq struct {
	auth, model, lang, prompt, filename string
	wavRate                             uint32
	wavSamples                          int
}

// whisperFake answers like an OpenAI / whisper.cpp server and records the
// request fields and the uploaded WAV's header.
func whisperFake(t *testing.T, text string, status int) (*httptest.Server, *[]seenReq, *sync.Mutex) {
	t.Helper()
	var (
		mu   sync.Mutex
		seen []seenReq
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil {
			t.Errorf("content-type: %v", err)
		}
		mr := multipart.NewReader(r.Body, params["boundary"])
		sr := seenReq{auth: r.Header.Get("Authorization")}
		for {
			p, err := mr.NextPart()
			if err == io.EOF {
				break
			}
			b, _ := io.ReadAll(p)
			switch p.FormName() {
			case "model":
				sr.model = string(b)
			case "language":
				sr.lang = string(b)
			case "prompt":
				sr.prompt = string(b)
			case "file":
				sr.filename = p.FileName()
				if len(b) >= 44 && string(b[:4]) == "RIFF" {
					sr.wavRate = binary.LittleEndian.Uint32(b[24:28])
					sr.wavSamples = int(binary.LittleEndian.Uint32(b[40:44])) / 2
				}
			}
		}
		mu.Lock()
		seen = append(seen, sr)
		mu.Unlock()
		if status != 200 {
			http.Error(w, `{"error":{"message":"rate limited"}}`, status)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"text": "` + text + `"}`))
	}))
	return srv, &seen, &mu
}

// TestManagerTranscribesCompletedRecordings: a call.complete with a
// recording is converted to 16 kHz WAV, posted with model / language /
// prompt / bearer, stored against the path and published as call.transcript;
// filters (system, talkgroup, encrypted, too short) skip; a failing server
// is counted, not fatal.
func TestManagerTranscribesCompletedRecordings(t *testing.T) {
	srv, seen, mu := whisperFake(t, " Engine one responding. ", 200)
	defer srv.Close()
	dir := t.TempDir()
	wav := writeWAV8k(t, dir, 8000*3) // 3 s at 8 kHz
	bus := events.NewBus(16)
	defer bus.Close()
	store := &fakeStore{}
	yes := true
	m, err := NewManager(Options{Bus: bus, Store: store, HTTP: srv.Client(), Config: config.TranscriptionConfig{
		Enabled: true, URL: srv.URL, APIKey: "sk-test", Model: "whisper-1", Language: "en", Prompt: "Engine 1",
		Systems: []string{"Metro"}, MinDurationMs: 1000, SkipEncrypted: &yes,
	}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go m.Run(ctx)
	sub := bus.Subscribe()
	defer sub.Close()

	start := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	publish := func(sys string, tg uint32, enc bool, dur time.Duration, path string) {
		bus.Publish(events.Event{Kind: events.KindCallComplete, Payload: trunking.CallComplete{
			Grant:        trunking.Grant{System: sys, Protocol: "p25", GroupID: tg, SourceID: 7, Encrypted: enc},
			DeviceSerial: "V1", StartedAt: start, EndedAt: start.Add(dur), AudioPath: path, Segment: 0,
		}})
	}
	publish("County", 1, false, 3*time.Second, wav)       // wrong system → skipped
	publish("Metro", 1, true, 3*time.Second, wav)         // encrypted → skipped
	publish("Metro", 1, false, 500*time.Millisecond, wav) // too short → skipped
	publish("Metro", 1001, false, 3*time.Second, wav)     // transcribed

	var tr trunking.CallTranscript
	deadline := time.After(5 * time.Second)
	for got := false; !got; {
		select {
		case ev := <-sub.C:
			if ev.Kind == events.KindCallTranscript {
				tr = ev.Payload.(trunking.CallTranscript)
				got = true
			}
		case <-deadline:
			t.Fatal("no call.transcript published")
		}
	}
	if tr.Text != "Engine one responding." || tr.GroupID != 1001 || tr.System != "Metro" || tr.AudioPath != wav || !tr.CallStartedAt.Equal(start) {
		t.Fatalf("transcript event = %+v", tr)
	}
	store.mu.Lock()
	stored := store.rows[wav]
	store.mu.Unlock()
	if stored != "Engine one responding." {
		t.Fatalf("stored = %q", stored)
	}
	mu.Lock()
	reqs := append([]seenReq(nil), (*seen)...)
	mu.Unlock()
	if len(reqs) != 1 {
		t.Fatalf("requests = %d, want 1 (filters must skip the other three)", len(reqs))
	}
	r := reqs[0]
	if r.auth != "Bearer sk-test" || r.model != "whisper-1" || r.lang != "en" || r.prompt != "Engine 1" || r.filename != "call.wav" {
		t.Fatalf("request fields = %+v", r)
	}
	if r.wavRate != 16000 || r.wavSamples != 16000*3 {
		t.Fatalf("uploaded WAV = %d Hz, %d samples; want 16 kHz, 48000", r.wavRate, r.wavSamples)
	}
	st := m.Status()
	if st.Sent != 1 || st.Skipped != 3 || st.Failed != 0 || st.LastText != "Engine one responding." || st.AudioSeconds != 3 {
		t.Fatalf("status = %+v", st)
	}
	m.Close()

	// A failing server is counted and reported, never fatal.
	bad, _, _ := whisperFake(t, "", 429)
	defer bad.Close()
	m2, _ := NewManager(Options{Bus: bus, HTTP: bad.Client(), Config: config.TranscriptionConfig{Enabled: true, URL: bad.URL}})
	m2.process(trunking.CallComplete{Grant: trunking.Grant{System: "X"}, StartedAt: start, EndedAt: start.Add(2 * time.Second), AudioPath: wav})
	if st := m2.Status(); st.Failed != 1 || !strings.Contains(st.LastError, "429") {
		t.Fatalf("failure status = %+v", st)
	}
	m2.Close()
}

// TestResample16k pins the converter: 8 kHz doubles the sample count with
// interpolated midpoints, 48 kHz triples down by averaging, 16 kHz is a copy.
func TestResample16k(t *testing.T) {
	in := []int16{0, 100, 200, 300}
	up := Resample16k(in, 8000)
	if len(up) != 8 || up[0] != 0 || up[1] != 50 || up[2] != 100 || up[3] != 150 || up[6] != 300 {
		t.Fatalf("8k→16k = %v", up)
	}
	down := Resample16k([]int16{10, 20, 30, 40, 50, 60}, 48000)
	if len(down) != 2 || down[0] != 20 || down[1] != 50 {
		t.Fatalf("48k→16k = %v", down)
	}
	same := Resample16k(in, 16000)
	if len(same) != 4 || same[3] != 300 {
		t.Fatalf("16k→16k = %v", same)
	}
	// WAV16k reads a flac/wav recording and emits a 16 kHz header.
	p := writeWAV8k(t, t.TempDir(), 800)
	b, err := WAV16k(p)
	if err != nil {
		t.Fatal(err)
	}
	if binary.LittleEndian.Uint32(b[24:28]) != 16000 || binary.LittleEndian.Uint32(b[40:44]) != 1600*2 {
		t.Fatalf("WAV16k header: rate=%d data=%d", binary.LittleEndian.Uint32(b[24:28]), binary.LittleEndian.Uint32(b[40:44]))
	}
}

// TestPlainTextResponse: a whisper.cpp server answering text/plain is
// accepted too.
func TestPlainTextResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte(" plain words\n"))
	}))
	defer srv.Close()
	bus := events.NewBus(1)
	defer bus.Close()
	m, _ := NewManager(Options{Bus: bus, HTTP: srv.Client(), Config: config.TranscriptionConfig{Enabled: true, URL: srv.URL, UploadFormat: "original"}})
	defer m.Close()
	p := writeWAV8k(t, t.TempDir(), 800)
	got, err := m.Transcribe(context.Background(), p)
	if err != nil || got != "plain words" {
		t.Fatalf("got %q, %v", got, err)
	}
}
