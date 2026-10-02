// Package alerts turns bus events into operator notifications: a rule
// engine (which events, under what conditions, with what cooldown) feeding
// notification channels (Discord, Slack, ntfy, Pushover, Telegram, Gotify, a
// generic webhook, a local command, an MQTT broker).
//
// It is the "alert me when talkgroup X keys up / on every emergency / when
// the tone-out matches" feature hardware scanners (Uniden custom alerts,
// Whistler alert LED) and SDRTrunk (alias actions) ship, and the push
// integrations trunk-recorder's community plugins add — none of which
// GopherTrunk had: the only outbound route was the per-completed-call
// webhook, and tone.alert never left the bus.
package alerts

import (
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/MattCheramie/GopherTrunk/internal/events"
	"github.com/MattCheramie/GopherTrunk/internal/radio/p25"
	"github.com/MattCheramie/GopherTrunk/internal/trunking"
	"github.com/MattCheramie/GopherTrunk/internal/voice/toneout"
)

// Event is the protocol-neutral view of a bus event the rules match on
// and the message templates render. Zero values mean "not applicable".
type Event struct {
	Kind     string    `json:"kind"`
	At       time.Time `json:"at"`
	System   string    `json:"system,omitempty"`
	Protocol string    `json:"protocol,omitempty"`
	// Talkgroup is the call's talkgroup (or the called radio on an
	// individual call); PatchedGroups the supergroup's members when the
	// call rode a patch.
	Talkgroup      uint32   `json:"talkgroup,omitempty"`
	TalkgroupAlpha string   `json:"talkgroup_alpha,omitempty"`
	PatchedGroups  []uint32 `json:"patched_groups,omitempty"`
	Source         uint32   `json:"source,omitempty"`
	SourceAlpha    string   `json:"source_alpha,omitempty"`
	FrequencyHz    uint32   `json:"frequency_hz,omitempty"`
	Emergency      bool     `json:"emergency,omitempty"`
	Encrypted      bool     `json:"encrypted,omitempty"`
	AlgorithmID    uint8    `json:"algorithm_id,omitempty"`
	KeyID          uint16   `json:"key_id,omitempty"`
	Individual     bool     `json:"individual,omitempty"`
	DataCall       bool     `json:"data_call,omitempty"`
	Timeslot       uint8    `json:"timeslot,omitempty"`
	// Duration / EndReason are set on call.end and call.complete.
	Duration  time.Duration `json:"duration_ns,omitempty"`
	EndReason string        `json:"end_reason,omitempty"`
	// AudioPath is the finished recording a call.complete announces.
	AudioPath string `json:"audio_path,omitempty"`
	// ToneProfile / ToneAlpha are set on tone.alert.
	ToneProfile string    `json:"tone_profile,omitempty"`
	ToneAlpha   string    `json:"tone_alpha,omitempty"`
	ToneHz      []float64 `json:"tone_hz,omitempty"`
	// Alias is the decoded talker alias on talker.alias.
	Alias string `json:"alias,omitempty"`
	// Target is the called / commanded radio on call.alert, unit.ack /
	// unit.queued / unit.deny, unit.function, unit.monitor and unit.status.
	Target uint32 `json:"target,omitempty"`
	// Detail is the kind-specific text: a deny / queued reason, an
	// extended-function name, a status or message value.
	Detail string `json:"detail,omitempty"`
	// Latitude / Longitude are set on location.
	Latitude  float64 `json:"latitude,omitempty"`
	Longitude float64 `json:"longitude,omitempty"`
	Device    string  `json:"device,omitempty"`
	// Payload is the raw bus payload (for the webhook / MQTT JSON).
	Payload any `json:"-"`
}

// FrequencyMHz renders the frequency as MHz with 4 decimals ("" when 0).
func (e Event) FrequencyMHz() string {
	if e.FrequencyHz == 0 {
		return ""
	}
	return fmt.Sprintf("%.4f", float64(e.FrequencyHz)/1e6)
}

// Algorithm renders the encryption algorithm ("" when clear/unknown).
func (e Event) Algorithm() string {
	if !e.Encrypted && e.AlgorithmID == 0 {
		return ""
	}
	if e.AlgorithmID == 0 {
		return "encrypted"
	}
	return p25.AlgorithmName(e.AlgorithmID)
}

// DurationSeconds renders Duration as seconds with one decimal.
func (e Event) DurationSeconds() string {
	if e.Duration <= 0 {
		return ""
	}
	return fmt.Sprintf("%.1f", e.Duration.Seconds())
}

// TalkgroupLabel is "alpha (id)" or just the id.
func (e Event) TalkgroupLabel() string {
	if e.Talkgroup == 0 && e.TalkgroupAlpha == "" {
		return ""
	}
	if e.TalkgroupAlpha != "" {
		return fmt.Sprintf("%s (%d)", e.TalkgroupAlpha, e.Talkgroup)
	}
	return fmt.Sprintf("%d", e.Talkgroup)
}

// SourceLabel is "alias (id)" or just the id.
func (e Event) SourceLabel() string {
	if e.Source == 0 {
		return ""
	}
	if e.SourceAlpha != "" {
		return fmt.Sprintf("%s (%d)", e.SourceAlpha, e.Source)
	}
	return fmt.Sprintf("%d", e.Source)
}

// Normalize maps a bus event onto an Event. ok is false for kinds the
// rule engine does not watch.
func Normalize(ev events.Event) (Event, bool) {
	e := Event{Kind: string(ev.Kind), At: ev.Timestamp, Payload: ev.Payload}
	if e.At.IsZero() {
		e.At = time.Now()
	}
	grant := func(g trunking.Grant) {
		e.System, e.Protocol = g.System, g.Protocol
		e.Talkgroup, e.TalkgroupAlpha = g.GroupID, g.GroupLabel
		e.PatchedGroups = g.PatchedGroups
		e.Source, e.FrequencyHz = g.SourceID, g.FrequencyHz
		e.Emergency, e.Encrypted = g.Emergency, g.Encrypted
		e.AlgorithmID, e.KeyID = g.AlgorithmID, g.KeyID
		e.Individual, e.DataCall, e.Timeslot = g.Individual, g.DataCall, g.Timeslot
	}
	tg := func(t *trunking.TalkGroup) {
		if t != nil && t.AlphaTag != "" {
			e.TalkgroupAlpha = t.AlphaTag
		}
	}
	switch p := ev.Payload.(type) {
	case trunking.CallStart:
		if ev.Kind != events.KindCallStart {
			return e, false
		}
		grant(p.Grant)
		tg(p.Talkgroup)
		e.Device = p.DeviceSerial
	case trunking.CallEnd:
		if ev.Kind != events.KindCallEnd {
			return e, false
		}
		grant(p.Grant)
		tg(p.Talkgroup)
		e.Device = p.DeviceSerial
		e.EndReason = string(p.Reason)
		if !p.EndedAt.IsZero() && !p.StartedAt.IsZero() {
			e.Duration = p.EndedAt.Sub(p.StartedAt)
		}
	case trunking.CallComplete:
		if ev.Kind != events.KindCallComplete {
			return e, false
		}
		grant(p.Grant)
		tg(p.Talkgroup)
		e.Device = p.DeviceSerial
		e.EndReason = string(p.Reason)
		e.AudioPath = p.AudioPath
		if !p.EndedAt.IsZero() && !p.StartedAt.IsZero() {
			e.Duration = p.EndedAt.Sub(p.StartedAt)
		}
	case trunking.Grant:
		if ev.Kind != events.KindGrant {
			return e, false
		}
		grant(p)
		if !p.At.IsZero() {
			e.At = p.At
		}
	case toneout.Alert:
		e.System, e.Device = p.System, p.DeviceSerial
		e.ToneProfile, e.ToneAlpha, e.ToneHz = p.Profile, p.AlphaTag, p.FrequenciesHz
		if !p.MatchedAt.IsZero() {
			e.At = p.MatchedAt
		}
	case trunking.Affiliation:
		e.System, e.Protocol = p.System, p.Protocol
		e.Source, e.Talkgroup = p.SourceID, p.GroupID
	case trunking.UnitRegistration:
		e.System, e.Protocol, e.Source = p.System, p.Protocol, p.SourceID
	case trunking.Patch:
		e.System, e.Protocol = p.System, p.Protocol
		e.Talkgroup, e.PatchedGroups = p.SuperGroup, p.Members
		if !p.At.IsZero() {
			e.At = p.At
		}
	case trunking.CallEncryption:
		e.System, e.Protocol, e.Device = p.System, p.Protocol, p.DeviceSerial
		e.Talkgroup = p.GroupID
		e.Encrypted = p.AlgorithmID != p25.AlgorithmClear
		e.AlgorithmID, e.KeyID = p.AlgorithmID, p.KeyID
		if !p.At.IsZero() {
			e.At = p.At
		}
	case trunking.TalkerAlias:
		e.System, e.Protocol, e.Source, e.Alias = p.System, p.Protocol, p.SourceID, p.Alias
		if !p.At.IsZero() {
			e.At = p.At
		}
	case trunking.Location:
		e.System, e.Protocol, e.Source, e.Talkgroup = p.System, p.Protocol, p.RadioID, p.Talkgroup
		e.Latitude, e.Longitude = p.Latitude, p.Longitude
		if !p.At.IsZero() {
			e.At = p.At
		}
	case trunking.UnitStatus:
		e.System, e.Protocol, e.Source, e.Target = p.System, p.Protocol, p.SourceID, p.TargetID
		e.Detail = fmt.Sprintf("unit status %d, user status %d", p.UnitStatus, p.UserStatus)
		if !p.At.IsZero() {
			e.At = p.At
		}
	case trunking.UnitMessage:
		e.System, e.Protocol, e.Source, e.Talkgroup = p.System, p.Protocol, p.SourceID, p.GroupID
		e.Detail = fmt.Sprintf("message 0x%04X", p.Message)
		if !p.At.IsZero() {
			e.At = p.At
		}
	case trunking.CallAlert:
		e.System, e.Protocol, e.Source, e.Target = p.System, p.Protocol, p.SourceID, p.TargetID
		if !p.At.IsZero() {
			e.At = p.At
		}
	case trunking.UnitResponse:
		e.System, e.Protocol, e.Source, e.Target = p.System, p.Protocol, p.SourceID, p.TargetID
		e.Detail = p.ServiceName
		if p.ReasonName != "" {
			e.Detail += ": " + p.ReasonName
		}
		if !p.At.IsZero() {
			e.At = p.At
		}
	case trunking.UnitFunction:
		e.System, e.Protocol, e.Source, e.Target = p.System, p.Protocol, p.SourceID, p.TargetID
		e.Detail = p.FunctionName
		if !p.At.IsZero() {
			e.At = p.At
		}
	case trunking.UnitMonitor:
		e.System, e.Protocol, e.Source, e.Target = p.System, p.Protocol, p.SourceID, p.TargetID
		e.Detail = "remote monitor"
		if !p.At.IsZero() {
			e.At = p.At
		}
	default:
		switch ev.Kind {
		case events.KindCCLocked, events.KindCCLost:
			// Every protocol publishes its own lock-state struct; pull the
			// common fields reflectively rather than import them all.
			e.System = reflectString(ev.Payload, "System", "SystemName")
			e.FrequencyHz = reflectUint32(ev.Payload, "FrequencyHz", "Frequency", "ControlFrequencyHz")
		default:
			return e, false
		}
	}
	return e, true
}

func reflectString(v any, names ...string) string {
	rv := reflect.Indirect(reflect.ValueOf(v))
	if rv.Kind() != reflect.Struct {
		return ""
	}
	for _, n := range names {
		f := rv.FieldByName(n)
		if f.IsValid() && f.Kind() == reflect.String {
			return f.String()
		}
	}
	return ""
}

func reflectUint32(v any, names ...string) uint32 {
	rv := reflect.Indirect(reflect.ValueOf(v))
	if rv.Kind() != reflect.Struct {
		return 0
	}
	for _, n := range names {
		f := rv.FieldByName(n)
		if !f.IsValid() {
			continue
		}
		switch f.Kind() {
		case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
			return uint32(f.Uint())
		case reflect.Int, reflect.Int32, reflect.Int64:
			if f.Int() > 0 {
				return uint32(f.Int())
			}
		case reflect.Float64, reflect.Float32:
			if f.Float() > 0 {
				return uint32(f.Float())
			}
		}
	}
	return 0
}

// Title is a short per-kind headline.
func (e Event) Title() string {
	switch e.Kind {
	case "call.start":
		if e.Emergency {
			return "EMERGENCY call"
		}
		if e.Encrypted {
			return "Encrypted call"
		}
		return "Call"
	case "call.end":
		return "Call ended"
	case "call.complete":
		return "Recording"
	case "grant":
		if e.Emergency {
			return "EMERGENCY grant"
		}
		return "Grant"
	case "tone.alert":
		return "Tone-out"
	case "cc.locked":
		return "Control channel locked"
	case "cc.lost":
		return "Control channel LOST"
	case "affiliation":
		return "Affiliation"
	case "registration":
		return "Registration"
	case "patch":
		return "Patch"
	case "call.encryption":
		return "Encryption"
	case "talker.alias":
		return "Talker alias"
	case "location":
		return "Location"
	case "unit.status":
		return "Unit status"
	case "unit.message":
		return "Unit message"
	case "call.alert":
		return "Call alert"
	case "unit.ack":
		return "Acknowledged"
	case "unit.queued":
		return "Queued"
	case "unit.deny":
		return "DENIED"
	case "unit.function":
		if strings.Contains(e.Detail, "inhibit") {
			return "Radio INHIBIT"
		}
		return "Radio command"
	case "unit.monitor":
		return "Radio monitor"
	}
	return strings.ToUpper(e.Kind[:1]) + e.Kind[1:]
}
