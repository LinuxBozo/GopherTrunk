package alerts

import (
	"strings"
	"sync"
	"text/template"
	"time"

	"github.com/MattCheramie/GopherTrunk/internal/config"
)

// Rule is a compiled config.AlertRuleConfig.
type Rule struct {
	Name          string
	kinds         map[string]bool
	systems       map[string]bool
	talkgroups    map[uint32]bool
	radios        map[uint32]bool
	emergency     bool
	encrypted     string // "" | "only" | "exclude"
	toneProfiles  map[string]bool
	keywords      []string
	minDuration   time.Duration
	cooldown      time.Duration
	Channels      []string
	tmpl          *template.Template
	attachAudio   bool
	mu            sync.Mutex
	lastFired     map[string]time.Time
	fired, cooled int
}

// NewRule compiles cfg. A bad message template is an error so a typo is
// caught at start-up, not on the first alert.
func NewRule(cfg config.AlertRuleConfig) (*Rule, error) {
	r := &Rule{
		Name:         cfg.Name,
		kinds:        map[string]bool{},
		systems:      map[string]bool{},
		talkgroups:   map[uint32]bool{},
		radios:       map[uint32]bool{},
		emergency:    cfg.Emergency,
		encrypted:    strings.ToLower(strings.TrimSpace(cfg.Encrypted)),
		toneProfiles: map[string]bool{},
		minDuration:  time.Duration(cfg.MinDurationMs) * time.Millisecond,
		cooldown:     cfg.CooldownDuration(),
		Channels:     append([]string(nil), cfg.Channels...),
		attachAudio:  cfg.AttachAudio,
		lastFired:    map[string]time.Time{},
	}
	for _, k := range cfg.EventKinds() {
		r.kinds[k] = true
	}
	for _, s := range cfg.Systems {
		r.systems[strings.ToLower(strings.TrimSpace(s))] = true
	}
	for _, t := range cfg.Talkgroups {
		r.talkgroups[t] = true
	}
	for _, id := range cfg.Radios {
		r.radios[id] = true
	}
	for _, p := range cfg.ToneProfiles {
		r.toneProfiles[strings.ToLower(strings.TrimSpace(p))] = true
	}
	for _, k := range cfg.Keywords {
		if k = strings.ToLower(strings.TrimSpace(k)); k != "" {
			r.keywords = append(r.keywords, k)
		}
	}
	if strings.TrimSpace(cfg.Message) != "" {
		t, err := template.New(cfg.Name).Option("missingkey=zero").Parse(cfg.Message)
		if err != nil {
			return nil, err
		}
		r.tmpl = t
	}
	return r, nil
}

// Matches reports whether e satisfies every condition of the rule (the
// cooldown is applied separately by Fire so a match can be counted even
// when suppressed).
func (r *Rule) Matches(e Event) bool {
	if !r.kinds[e.Kind] {
		return false
	}
	if len(r.systems) > 0 && !r.systems[strings.ToLower(e.System)] {
		return false
	}
	if len(r.talkgroups) > 0 {
		hit := r.talkgroups[e.Talkgroup]
		for _, g := range e.PatchedGroups {
			if r.talkgroups[g] {
				hit = true
			}
		}
		if !hit {
			return false
		}
	}
	if len(r.radios) > 0 && !r.radios[e.Source] {
		return false
	}
	if r.emergency && !e.Emergency {
		return false
	}
	switch r.encrypted {
	case "only":
		if !e.Encrypted {
			return false
		}
	case "exclude":
		if e.Encrypted {
			return false
		}
	}
	if e.Kind == "tone.alert" && len(r.toneProfiles) > 0 && !r.toneProfiles[strings.ToLower(e.ToneProfile)] {
		return false
	}
	if r.minDuration > 0 && (e.Kind == "call.end" || e.Kind == "call.complete") && e.Duration < r.minDuration {
		return false
	}
	if len(r.keywords) > 0 {
		text := strings.ToLower(e.Transcript + " " + e.Alias + " " + e.Detail)
		hit := false
		for _, k := range r.keywords {
			if strings.Contains(text, k) {
				hit = true
				break
			}
		}
		if !hit {
			return false
		}
	}
	return true
}

// cooldownKey groups repeat firings: one key per (system, talkgroup) or
// (system, tone profile), so a chatty talkgroup is throttled without
// hiding a different one.
func cooldownKey(e Event) string {
	switch e.Kind {
	case "tone.alert":
		return e.System + "|tone|" + e.ToneProfile
	case "cc.locked", "cc.lost":
		return e.System + "|cc"
	}
	return e.System + "|" + strings.ToLower(e.Kind) + "|" + uitoa(e.Talkgroup)
}

// Fire records a firing for e and reports whether it should be delivered
// (false while the cooldown for its key still runs).
func (r *Rule) Fire(e Event, now time.Time) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cooldown > 0 {
		k := cooldownKey(e)
		if last, ok := r.lastFired[k]; ok && now.Sub(last) < r.cooldown {
			r.cooled++
			return false
		}
		r.lastFired[k] = now
		// Bound the map: drop keys older than the cooldown.
		if len(r.lastFired) > 1024 {
			for kk, t := range r.lastFired {
				if now.Sub(t) >= r.cooldown {
					delete(r.lastFired, kk)
				}
			}
		}
	}
	r.fired++
	return true
}

// Stats returns the firing / cooldown-suppressed counters.
func (r *Rule) Stats() (fired, cooled int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.fired, r.cooled
}

// AttachAudio reports whether call.complete deliveries carry the recording.
func (r *Rule) AttachAudio() bool { return r.attachAudio }

func uitoa(v uint32) string {
	if v == 0 {
		return "0"
	}
	var b [10]byte
	i := len(b)
	for v > 0 {
		i--
		b[i] = byte('0' + v%10)
		v /= 10
	}
	return string(b[i:])
}
