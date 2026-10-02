// Package transcribe sends finished call recordings to a Whisper-compatible
// speech-to-text server and stores the text with the call: the
// "transcripts" column every call-playback front end has grown, and a
// keyword hook for alerts. One HTTP client covers OpenAI's audio API, a
// local whisper.cpp server, faster-whisper / Speaches and LocalAI because
// they all take the same multipart request and answer {"text": …}.
package transcribe

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/MattCheramie/GopherTrunk/internal/config"
	"github.com/MattCheramie/GopherTrunk/internal/events"
	"github.com/MattCheramie/GopherTrunk/internal/trunking"
	"github.com/MattCheramie/GopherTrunk/internal/voice"
)

// Store persists a transcript against the call that owns a recording file.
type Store interface {
	SetTranscript(ctx context.Context, recordingPath, text string) error
}

// Options configure a Manager.
type Options struct {
	Bus    *events.Bus
	Config config.TranscriptionConfig
	Store  Store // may be nil (transcripts are still published on the bus)
	Log    *slog.Logger
	HTTP   *http.Client
	Now    func() time.Time
}

// Manager subscribes to call.complete, filters, converts the audio and
// posts it; results land in the Store and on the bus as call.transcript.
type Manager struct {
	bus   *events.Bus
	cfg   config.TranscriptionConfig
	store Store
	log   *slog.Logger
	http  *http.Client
	now   func() time.Time

	systems    map[string]bool
	talkgroups map[uint32]bool

	sub     *events.Subscription
	jobs    chan trunking.CallComplete
	wg      sync.WaitGroup
	runDone chan struct{}
	once    sync.Once

	mu                                     sync.Mutex
	queued, dropped, sent, failed, skipped int
	lastErr                                string
	lastText                               string
	lastAt                                 time.Time
	totalAudio                             time.Duration
	totalLatency                           time.Duration
}

const queueDepth = 128

// NewManager compiles the config and subscribes to the bus.
func NewManager(opts Options) (*Manager, error) {
	if opts.Bus == nil {
		return nil, errors.New("transcribe: events.Bus is required")
	}
	if strings.TrimSpace(opts.Config.URL) == "" {
		return nil, errors.New("transcribe: url is required")
	}
	if opts.Log == nil {
		opts.Log = slog.Default()
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.HTTP == nil {
		opts.HTTP = &http.Client{Timeout: opts.Config.TimeoutDuration()}
	}
	m := &Manager{
		bus: opts.Bus, cfg: opts.Config, store: opts.Store, log: opts.Log, http: opts.HTTP, now: opts.Now,
		systems: map[string]bool{}, talkgroups: map[uint32]bool{},
		jobs: make(chan trunking.CallComplete, queueDepth), runDone: make(chan struct{}),
	}
	for _, s := range opts.Config.Systems {
		m.systems[strings.ToLower(strings.TrimSpace(s))] = true
	}
	for _, t := range opts.Config.Talkgroups {
		m.talkgroups[t] = true
	}
	m.sub = opts.Bus.Subscribe()
	for i := 0; i < opts.Config.WorkersOrDefault(); i++ {
		m.wg.Add(1)
		go m.worker()
	}
	return m, nil
}

// Run drains call.complete events until ctx is cancelled.
func (m *Manager) Run(ctx context.Context) error {
	defer close(m.runDone)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case ev, ok := <-m.sub.C:
			if !ok {
				return nil
			}
			if ev.Kind != events.KindCallComplete {
				continue
			}
			if cc, ok := ev.Payload.(trunking.CallComplete); ok {
				m.Handle(cc)
			}
		}
	}
}

// Handle applies the filters and queues one finished recording.
func (m *Manager) Handle(cc trunking.CallComplete) {
	if cc.AudioPath == "" {
		return
	}
	if len(m.systems) > 0 && !m.systems[strings.ToLower(cc.Grant.System)] {
		m.count(&m.skipped)
		return
	}
	if len(m.talkgroups) > 0 && !m.talkgroups[cc.Grant.GroupID] {
		m.count(&m.skipped)
		return
	}
	if m.cfg.SkipsEncrypted() && cc.Grant.Encrypted {
		m.count(&m.skipped)
		return
	}
	if d := cc.EndedAt.Sub(cc.StartedAt); !cc.EndedAt.IsZero() && d < m.cfg.MinDuration() {
		m.count(&m.skipped)
		return
	}
	select {
	case m.jobs <- cc:
		m.count(&m.queued)
	default:
		m.count(&m.dropped)
		m.log.Warn("transcribe: queue full, dropping recording", "path", cc.AudioPath)
	}
}

func (m *Manager) count(p *int) {
	m.mu.Lock()
	*p++
	m.mu.Unlock()
}

func (m *Manager) worker() {
	defer m.wg.Done()
	for cc := range m.jobs {
		m.process(cc)
	}
}

func (m *Manager) process(cc trunking.CallComplete) {
	ctx, cancel := context.WithTimeout(context.Background(), m.cfg.TimeoutDuration()+5*time.Second)
	defer cancel()
	start := m.now()
	text, err := m.Transcribe(ctx, cc.AudioPath)
	if err != nil {
		m.mu.Lock()
		m.failed++
		m.lastErr = err.Error()
		m.mu.Unlock()
		m.log.Warn("transcribe: request failed", "path", filepath.Base(cc.AudioPath), "err", err)
		return
	}
	lat := m.now().Sub(start)
	m.mu.Lock()
	m.sent++
	m.lastText, m.lastAt = text, m.now()
	m.totalLatency += lat
	if !cc.EndedAt.IsZero() {
		m.totalAudio += cc.EndedAt.Sub(cc.StartedAt)
	}
	m.mu.Unlock()
	if m.store != nil {
		if err := m.store.SetTranscript(ctx, cc.AudioPath, text); err != nil {
			m.log.Warn("transcribe: store failed", "path", cc.AudioPath, "err", err)
		}
	}
	m.bus.Publish(events.Event{Kind: events.KindCallTranscript, Payload: trunking.CallTranscript{
		System: cc.Grant.System, Protocol: cc.Grant.Protocol, GroupID: cc.Grant.GroupID,
		SourceID: cc.Grant.SourceID, FrequencyHz: cc.Grant.FrequencyHz,
		DeviceSerial: cc.DeviceSerial, CallStartedAt: cc.CallStart(), Segment: cc.Segment,
		AudioPath: cc.AudioPath, Text: text, At: m.now(),
	}})
	m.log.Info("transcribe: transcript", "system", cc.Grant.System, "tg", cc.Grant.GroupID,
		"segment", cc.Segment, "latency", lat.Round(time.Millisecond), "text", truncate(text, 120))
}

// Transcribe posts one recording and returns the text (exported for the
// API's test hook and offline tooling).
func (m *Manager) Transcribe(ctx context.Context, path string) (string, error) {
	var (
		body     []byte
		filename string
		err      error
	)
	if strings.ToLower(strings.TrimSpace(m.cfg.UploadFormat)) == "original" {
		body, err = os.ReadFile(path)
		filename = filepath.Base(path)
	} else {
		body, err = WAV16k(path)
		filename = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)) + ".wav"
	}
	if err != nil {
		return "", err
	}
	if len(body) == 0 {
		return "", errors.New("transcribe: empty recording")
	}
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("model", m.cfg.ModelOrDefault())
	_ = mw.WriteField("response_format", "json")
	if m.cfg.Language != "" {
		_ = mw.WriteField("language", m.cfg.Language)
	}
	if m.cfg.Prompt != "" {
		_ = mw.WriteField("prompt", m.cfg.Prompt)
	}
	part, err := mw.CreateFormFile("file", filename)
	if err != nil {
		return "", err
	}
	if _, err := part.Write(body); err != nil {
		return "", err
	}
	if err := mw.Close(); err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.cfg.URL, &buf)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	if m.cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+m.cfg.APIKey)
	}
	resp, err := m.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		line := strings.TrimSpace(strings.SplitN(string(raw), "\n", 2)[0])
		return "", fmt.Errorf("transcribe: HTTP %d %s", resp.StatusCode, truncate(line, 200))
	}
	var out struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		// whisper.cpp with response_format=text answers plain text.
		if t := strings.TrimSpace(string(raw)); t != "" && !strings.HasPrefix(t, "{") {
			return t, nil
		}
		return "", fmt.Errorf("transcribe: bad response: %w", err)
	}
	return strings.TrimSpace(out.Text), nil
}

// WAV16k reads any recording container (wav / flac via voice.ReadAudioSamples)
// and returns a 16 kHz mono 16-bit PCM WAV — the one input every Whisper
// server accepts (whisper.cpp refuses anything else; OpenAI resamples but
// 16 kHz halves the upload for an 8 kHz recording, which is upsampled by
// linear interpolation — speech-recognition-grade, no spectral content
// exists above 4 kHz to protect).
func WAV16k(path string) ([]byte, error) {
	samples, rate, err := voice.ReadAudioSamples(path)
	if err != nil {
		return nil, err
	}
	if rate == 0 {
		return nil, errors.New("transcribe: recording has no sample rate")
	}
	out := Resample16k(samples, int(rate))
	return wavBytes(out, 16000), nil
}

// Resample16k converts samples at rate to 16 kHz by linear interpolation
// (upsampling) or decimation with averaging (downsampling).
func Resample16k(in []int16, rate int) []int16 {
	const target = 16000
	if rate == target || len(in) == 0 {
		return append([]int16(nil), in...)
	}
	n := int(int64(len(in)) * target / int64(rate))
	out := make([]int16, n)
	if rate < target {
		for i := range out {
			pos := float64(i) * float64(rate) / float64(target)
			j := int(pos)
			frac := pos - float64(j)
			a := float64(in[j])
			b := a
			if j+1 < len(in) {
				b = float64(in[j+1])
			}
			out[i] = int16(a + (b-a)*frac)
		}
		return out
	}
	// Downsample: average the span each output sample covers.
	step := float64(rate) / float64(target)
	for i := range out {
		lo := int(float64(i) * step)
		hi := int(float64(i+1) * step)
		if hi > len(in) {
			hi = len(in)
		}
		if hi <= lo {
			hi = lo + 1
		}
		var acc int64
		for j := lo; j < hi && j < len(in); j++ {
			acc += int64(in[j])
		}
		out[i] = int16(acc / int64(hi-lo))
	}
	return out
}

func wavBytes(samples []int16, rate int) []byte {
	data := len(samples) * 2
	buf := make([]byte, 44+data)
	copy(buf[0:], "RIFF")
	binary.LittleEndian.PutUint32(buf[4:], uint32(36+data))
	copy(buf[8:], "WAVE")
	copy(buf[12:], "fmt ")
	binary.LittleEndian.PutUint32(buf[16:], 16)
	binary.LittleEndian.PutUint16(buf[20:], 1)
	binary.LittleEndian.PutUint16(buf[22:], 1)
	binary.LittleEndian.PutUint32(buf[24:], uint32(rate))
	binary.LittleEndian.PutUint32(buf[28:], uint32(rate*2))
	binary.LittleEndian.PutUint16(buf[32:], 2)
	binary.LittleEndian.PutUint16(buf[34:], 16)
	copy(buf[36:], "data")
	binary.LittleEndian.PutUint32(buf[40:], uint32(data))
	for i, s := range samples {
		binary.LittleEndian.PutUint16(buf[44+2*i:], uint16(s))
	}
	return buf
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// Status is the GET /api/v1/transcription snapshot.
type Status struct {
	URL          string        `json:"url"`
	Model        string        `json:"model"`
	Language     string        `json:"language,omitempty"`
	Queued       int           `json:"queued"`
	Dropped      int           `json:"dropped"`
	Sent         int           `json:"sent"`
	Failed       int           `json:"failed"`
	Skipped      int           `json:"skipped"`
	LastError    string        `json:"last_error,omitempty"`
	LastText     string        `json:"last_text,omitempty"`
	LastAt       time.Time     `json:"last_at,omitempty"`
	AudioSeconds float64       `json:"audio_seconds"`
	MeanLatency  time.Duration `json:"mean_latency_ns"`
}

// Status returns counters for the API.
func (m *Manager) Status() Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	st := Status{URL: m.cfg.URL, Model: m.cfg.ModelOrDefault(), Language: m.cfg.Language,
		Queued: m.queued, Dropped: m.dropped, Sent: m.sent, Failed: m.failed, Skipped: m.skipped,
		LastError: m.lastErr, LastText: m.lastText, LastAt: m.lastAt, AudioSeconds: m.totalAudio.Seconds()}
	if m.sent > 0 {
		st.MeanLatency = m.totalLatency / time.Duration(m.sent)
	}
	return st
}

// Close stops the subscription and drains the queue.
func (m *Manager) Close() error {
	m.once.Do(func() {
		m.sub.Close()
		select {
		case <-m.runDone:
		case <-time.After(2 * time.Second):
		}
		close(m.jobs)
		m.wg.Wait()
	})
	return nil
}
