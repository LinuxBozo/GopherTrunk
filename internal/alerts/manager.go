package alerts

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/MattCheramie/GopherTrunk/internal/config"
	"github.com/MattCheramie/GopherTrunk/internal/events"
)

// Options configure a Manager.
type Options struct {
	Bus     *events.Bus
	Config  config.AlertsConfig
	Log     *slog.Logger
	HTTP    *http.Client // nil = per-channel default with the channel timeout
	Workers int          // delivery goroutines (default 2)
	Now     func() time.Time
	// EventJSON renders a bus event for MQTT mirroring (the daemon passes the
	// API's SSE encoder so the topics carry the same JSON the web UI sees).
	// nil falls back to encoding/json of {kind, timestamp, payload}.
	EventJSON func(events.Event) ([]byte, error)
	// SourceAlias, when set, resolves a radio ID to its alias for messages.
	SourceAlias func(system string, id uint32) string
}

// Manager subscribes to the bus, evaluates rules and delivers through
// channels on a bounded worker queue. A slow or dead destination never
// blocks the bus: the queue drops (and counts) when full.
type Manager struct {
	bus       *events.Bus
	log       *slog.Logger
	now       func() time.Time
	channels  map[string]Channel
	order     []string
	rules     []*Rule
	eventJSON func(events.Event) ([]byte, error)
	srcAlias  func(string, uint32) string
	mirrors   []*mqttChannel

	sub     *events.Subscription
	jobs    chan job
	wg      sync.WaitGroup
	runDone chan struct{}
	once    sync.Once

	mu       sync.Mutex
	matched  int
	queued   int
	dropped  int
	sent     map[string]int
	failed   map[string]int
	lastErr  map[string]string
	lastSent map[string]time.Time
	recent   []Firing
}

type job struct {
	channel string
	n       Notification
}

// Firing is one recent rule firing for the status API.
type Firing struct {
	Rule     string    `json:"rule"`
	At       time.Time `json:"at"`
	Title    string    `json:"title"`
	Text     string    `json:"text"`
	Channels []string  `json:"channels"`
}

const (
	defaultWorkers    = 2
	defaultQueueDepth = 256
	recentFirings     = 50
)

// NewManager compiles the config, builds the channels and subscribes to the
// bus (so events published before Run are not lost).
func NewManager(opts Options) (*Manager, error) {
	if opts.Bus == nil {
		return nil, errors.New("alerts: events.Bus is required")
	}
	if opts.Log == nil {
		opts.Log = slog.Default()
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Workers <= 0 {
		opts.Workers = defaultWorkers
	}
	m := &Manager{
		bus: opts.Bus, log: opts.Log, now: opts.Now,
		channels:  map[string]Channel{},
		eventJSON: opts.EventJSON,
		srcAlias:  opts.SourceAlias,
		jobs:      make(chan job, defaultQueueDepth),
		runDone:   make(chan struct{}),
		sent:      map[string]int{}, failed: map[string]int{},
		lastErr: map[string]string{}, lastSent: map[string]time.Time{},
	}
	for _, cc := range opts.Config.Channels {
		ch, err := NewChannel(cc, opts.HTTP)
		if err != nil {
			return nil, fmt.Errorf("alerts.channels %q: %w", cc.Name, err)
		}
		m.channels[cc.Name] = ch
		m.order = append(m.order, cc.Name)
		if mc, ok := ch.(*mqttChannel); ok && mc.MirrorsEvents() {
			m.mirrors = append(m.mirrors, mc)
		}
	}
	for _, rc := range opts.Config.Rules {
		if rc.Disabled {
			continue
		}
		r, err := NewRule(rc)
		if err != nil {
			return nil, fmt.Errorf("alerts.rules %q: message template: %w", rc.Name, err)
		}
		for _, cn := range r.Channels {
			if _, ok := m.channels[cn]; !ok {
				return nil, fmt.Errorf("alerts.rules %q: channel %q not defined", rc.Name, cn)
			}
		}
		m.rules = append(m.rules, r)
	}
	m.sub = opts.Bus.Subscribe()
	for i := 0; i < opts.Workers; i++ {
		m.wg.Add(1)
		go m.worker()
	}
	return m, nil
}

// Rules / Channels report the compiled counts.
func (m *Manager) Rules() int    { return len(m.rules) }
func (m *Manager) Channels() int { return len(m.channels) }

// Run drains the bus until ctx is cancelled or the subscription closes.
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
			m.Handle(ev)
		}
	}
}

// Handle evaluates one bus event against every rule (exported for tests
// and for callers that feed events directly).
func (m *Manager) Handle(ev events.Event) {
	if len(m.mirrors) > 0 {
		m.mirror(ev)
	}
	e, ok := Normalize(ev)
	if !ok {
		return
	}
	if m.srcAlias != nil && e.Source != 0 && e.SourceAlpha == "" {
		e.SourceAlpha = m.srcAlias(e.System, e.Source)
	}
	now := m.now()
	for _, r := range m.rules {
		if !r.Matches(e) {
			continue
		}
		m.mu.Lock()
		m.matched++
		m.mu.Unlock()
		if !r.Fire(e, now) {
			continue
		}
		n := r.Render(e)
		ensureAudio(&n)
		m.record(Firing{Rule: r.Name, At: now, Title: n.Title, Text: n.Text, Channels: r.Channels})
		for _, cn := range r.Channels {
			m.enqueue(job{channel: cn, n: n})
		}
	}
}

func (m *Manager) mirror(ev events.Event) {
	var (
		b   []byte
		err error
	)
	if m.eventJSON != nil {
		b, err = m.eventJSON(ev)
	} else {
		b, err = json.Marshal(struct {
			Kind      string    `json:"kind"`
			Timestamp time.Time `json:"timestamp"`
			Payload   any       `json:"payload"`
		}{string(ev.Kind), ev.Timestamp, ev.Payload})
	}
	if err != nil {
		return
	}
	for _, mc := range m.mirrors {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		if perr := mc.PublishEvent(ctx, string(ev.Kind), b); perr != nil {
			m.noteFailure(mc.Name(), perr)
		}
		cancel()
	}
}

func (m *Manager) enqueue(j job) {
	select {
	case m.jobs <- j:
		m.mu.Lock()
		m.queued++
		m.mu.Unlock()
	default:
		m.mu.Lock()
		m.dropped++
		m.mu.Unlock()
		m.log.Warn("alerts: delivery queue full, dropping alert", "rule", j.n.Rule, "channel", j.channel)
	}
}

func (m *Manager) worker() {
	defer m.wg.Done()
	for j := range m.jobs {
		ch, ok := m.channels[j.channel]
		if !ok {
			continue
		}
		m.deliver(ch, j.n)
	}
}

func (m *Manager) deliver(ch Channel, n Notification) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	err := ch.Send(ctx, n)
	if err == nil {
		m.mu.Lock()
		m.sent[ch.Name()]++
		m.lastSent[ch.Name()] = m.now()
		m.mu.Unlock()
		m.log.Debug("alerts: delivered", "rule", n.Rule, "channel", ch.Name(), "type", ch.Type())
		return
	}
	m.noteFailure(ch.Name(), err)
	m.log.Warn("alerts: delivery failed", "rule", n.Rule, "channel", ch.Name(), "type", ch.Type(), "err", err)
}

func (m *Manager) noteFailure(channel string, err error) {
	m.mu.Lock()
	m.failed[channel]++
	m.lastErr[channel] = err.Error()
	m.mu.Unlock()
}

func (m *Manager) record(f Firing) {
	m.mu.Lock()
	m.recent = append(m.recent, f)
	if len(m.recent) > recentFirings {
		m.recent = m.recent[len(m.recent)-recentFirings:]
	}
	m.mu.Unlock()
}

// Test delivers a synthetic notification through one channel right away
// (bypassing rules and the queue) so an operator can prove a channel works
// from the API.
func (m *Manager) Test(ctx context.Context, channel string) error {
	ch, ok := m.channels[channel]
	if !ok {
		return fmt.Errorf("alerts: no channel %q", channel)
	}
	now := m.now()
	n := Notification{
		Rule:  "test",
		Title: "GopherTrunk test alert",
		Text:  "Channel " + channel + " (" + ch.Type() + ") is reachable — " + now.Format(time.RFC3339),
		At:    now,
		Event: Event{Kind: "test", At: now},
	}
	err := ch.Send(ctx, n)
	if err != nil {
		m.noteFailure(channel, err)
		return err
	}
	m.mu.Lock()
	m.sent[channel]++
	m.lastSent[channel] = now
	m.mu.Unlock()
	return nil
}

// ChannelStatus is one channel's delivery counters.
type ChannelStatus struct {
	Name      string    `json:"name"`
	Type      string    `json:"type"`
	Sent      int       `json:"sent"`
	Failed    int       `json:"failed"`
	LastError string    `json:"last_error,omitempty"`
	LastSent  time.Time `json:"last_sent,omitempty"`
	Mirrors   bool      `json:"mirrors_events,omitempty"`
}

// RuleStatus is one rule's counters.
type RuleStatus struct {
	Name     string   `json:"name"`
	Channels []string `json:"channels"`
	Fired    int      `json:"fired"`
	Cooled   int      `json:"cooldown_suppressed"`
}

// Status is the GET /api/v1/alerts snapshot.
type Status struct {
	Channels []ChannelStatus `json:"channels"`
	Rules    []RuleStatus    `json:"rules"`
	Matched  int             `json:"matched"`
	Queued   int             `json:"queued"`
	Dropped  int             `json:"dropped"`
	Recent   []Firing        `json:"recent"`
}

// Status returns the live snapshot.
func (m *Manager) Status() Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	st := Status{Matched: m.matched, Queued: m.queued, Dropped: m.dropped}
	names := append([]string(nil), m.order...)
	sort.Strings(names)
	for _, n := range names {
		ch := m.channels[n]
		cs := ChannelStatus{Name: n, Type: ch.Type(), Sent: m.sent[n], Failed: m.failed[n], LastError: m.lastErr[n], LastSent: m.lastSent[n]}
		if mc, ok := ch.(*mqttChannel); ok {
			cs.Mirrors = mc.MirrorsEvents()
		}
		st.Channels = append(st.Channels, cs)
	}
	for _, r := range m.rules {
		f, c := r.Stats()
		st.Rules = append(st.Rules, RuleStatus{Name: r.Name, Channels: r.Channels, Fired: f, Cooled: c})
	}
	st.Recent = append([]Firing(nil), m.recent...)
	if st.Channels == nil {
		st.Channels = []ChannelStatus{}
	}
	if st.Rules == nil {
		st.Rules = []RuleStatus{}
	}
	if st.Recent == nil {
		st.Recent = []Firing{}
	}
	return st
}

// Close stops the bus subscription, drains the queue and closes channels
// that hold connections.
func (m *Manager) Close() error {
	m.once.Do(func() {
		m.sub.Close()
		select {
		case <-m.runDone:
		case <-time.After(2 * time.Second):
		}
		close(m.jobs)
		m.wg.Wait()
		for _, ch := range m.channels {
			if c, ok := ch.(interface{ Close() error }); ok {
				_ = c.Close()
			}
		}
	})
	return nil
}
