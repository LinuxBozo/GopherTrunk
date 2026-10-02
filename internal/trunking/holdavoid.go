package trunking

import (
	"strings"
	"sync"
	"time"
)

// Talkgroup HOLD and timed AVOID — the two scanner-front-panel controls
// every hardware scanner has (Uniden "Hold" / "Avoid" with the one-press
// temporary form, Whistler lockout, rdio-scanner's HOLD TG / timed AVOID)
// and GopherTrunk only approximated by flipping a talkgroup's permanent
// Lockout or Scan flag.
//
//   - Hold pins the engine to ONE talkgroup: every other grant is dropped
//     until the hold is released (emergency grants still pass, matching the
//     Lockout / scan-list precedent). A held talkgroup also bypasses the
//     scan-list gate, so "hold on this TG" works in list mode too.
//   - Avoid is a lockout with an expiry: the talkgroup is dropped until the
//     deadline passes, then scanning resumes on its own. Nothing is written
//     to the catalogue, so a restart clears it like a scanner power cycle.
//
// Both are keyed by (system, talkgroup); an empty system matches any.

// HoldState is the engine's active hold, if any.
type HoldState struct {
	System    string    `json:"system,omitempty"`
	Talkgroup uint32    `json:"talkgroup"`
	Since     time.Time `json:"since"`
}

// Avoid is one temporary talkgroup lockout.
type Avoid struct {
	System    string    `json:"system,omitempty"`
	Talkgroup uint32    `json:"talkgroup"`
	Until     time.Time `json:"until"`
}

type avoidKey struct {
	system string
	tg     uint32
}

// holdAvoid is the engine's hold / avoid state. Separate from the grant
// gate's mutex so the API can read it without contending with decode.
type holdAvoid struct {
	mu     sync.Mutex
	hold   *HoldState
	avoids map[avoidKey]time.Time
	now    func() time.Time
}

func newHoldAvoid(now func() time.Time) *holdAvoid {
	if now == nil {
		now = time.Now
	}
	return &holdAvoid{avoids: map[avoidKey]time.Time{}, now: now}
}

func normSystem(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

// Hold pins the engine to one talkgroup (system "" = on any system).
func (h *holdAvoid) Hold(system string, tg uint32) HoldState {
	h.mu.Lock()
	defer h.mu.Unlock()
	st := HoldState{System: strings.TrimSpace(system), Talkgroup: tg, Since: h.now()}
	h.hold = &st
	return st
}

// Release clears the hold; reports whether one was active.
func (h *holdAvoid) Release() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	had := h.hold != nil
	h.hold = nil
	return had
}

// Held returns the active hold.
func (h *holdAvoid) Held() (HoldState, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.hold == nil {
		return HoldState{}, false
	}
	return *h.hold, true
}

// Avoid locks (system, tg) out until now+d. A zero or negative d clears an
// existing avoid.
func (h *holdAvoid) Avoid(system string, tg uint32, d time.Duration) Avoid {
	h.mu.Lock()
	defer h.mu.Unlock()
	k := avoidKey{normSystem(system), tg}
	if d <= 0 {
		delete(h.avoids, k)
		return Avoid{System: strings.TrimSpace(system), Talkgroup: tg}
	}
	until := h.now().Add(d)
	h.avoids[k] = until
	return Avoid{System: strings.TrimSpace(system), Talkgroup: tg, Until: until}
}

// Unavoid clears a temporary lockout; reports whether one existed.
func (h *holdAvoid) Unavoid(system string, tg uint32) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	k := avoidKey{normSystem(system), tg}
	_, ok := h.avoids[k]
	delete(h.avoids, k)
	return ok
}

// Avoids lists the live (unexpired) avoids, pruning expired ones.
func (h *holdAvoid) Avoids() []Avoid {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := h.now()
	out := make([]Avoid, 0, len(h.avoids))
	for k, until := range h.avoids {
		if !until.After(now) {
			delete(h.avoids, k)
			continue
		}
		out = append(out, Avoid{System: k.system, Talkgroup: k.tg, Until: until})
	}
	return out
}

// gate reports whether a grant on (system, tg, patched) may proceed: false
// when a hold names a different talkgroup, or an unexpired avoid covers the
// talkgroup (or every patched member). reason labels the drop for the log.
func (h *holdAvoid) gate(system string, tg uint32, patched []uint32) (ok bool, reason string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.hold != nil {
		if h.hold.System != "" && normSystem(h.hold.System) != normSystem(system) {
			return false, "hold"
		}
		if h.hold.Talkgroup != tg && !containsTG(patched, h.hold.Talkgroup) {
			return false, "hold"
		}
		return true, ""
	}
	if len(h.avoids) == 0 {
		return true, ""
	}
	now := h.now()
	avoided := func(g uint32) bool {
		for _, k := range []avoidKey{{normSystem(system), g}, {"", g}} {
			if until, ok := h.avoids[k]; ok {
				if until.After(now) {
					return true
				}
				delete(h.avoids, k)
			}
		}
		return false
	}
	if avoided(tg) {
		// A patched supergroup stays followable while any member is not avoided.
		for _, m := range patched {
			if !avoided(m) {
				return true, ""
			}
		}
		return false, "avoid"
	}
	return true, ""
}

func containsTG(xs []uint32, v uint32) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}

// Hold pins the engine to one talkgroup (see holdAvoid). system "" = any.
func (e *Engine) Hold(system string, tg uint32) HoldState { return e.holdAvoid.Hold(system, tg) }

// ReleaseHold clears the talkgroup hold.
func (e *Engine) ReleaseHold() bool { return e.holdAvoid.Release() }

// Held returns the active talkgroup hold.
func (e *Engine) Held() (HoldState, bool) { return e.holdAvoid.Held() }

// AvoidTalkgroup temporarily locks out (system, tg) for d.
func (e *Engine) AvoidTalkgroup(system string, tg uint32, d time.Duration) Avoid {
	return e.holdAvoid.Avoid(system, tg, d)
}

// UnavoidTalkgroup clears a temporary lockout.
func (e *Engine) UnavoidTalkgroup(system string, tg uint32) bool {
	return e.holdAvoid.Unavoid(system, tg)
}

// Avoids lists the live temporary lockouts.
func (e *Engine) Avoids() []Avoid { return e.holdAvoid.Avoids() }
