package config

import (
	"fmt"
	"strings"
	"time"
)

// AlertsConfig is the `alerts:` section: operator-defined alert RULES that
// watch the event bus (a talkgroup keying up, an emergency, an encrypted
// call, a tone-out match, a control channel lost, …) and the notification
// CHANNELS they fire into (Discord, Slack, ntfy, Pushover, Telegram,
// Gotify, a generic webhook, a local command, an MQTT broker).
//
// This is the "alert when TG X keys up / on any emergency" feature every
// hardware scanner (Uniden custom alerts, Whistler alert LED) and SDRTrunk
// (alias actions: beep / clip / script) has, and the push-notification
// integrations trunk-recorder's community plugins provide. Rules and
// channels are decoupled so one rule can fan out to several channels and
// one channel can serve several rules.
type AlertsConfig struct {
	Channels []AlertChannelConfig `yaml:"channels"`
	Rules    []AlertRuleConfig    `yaml:"rules"`
}

// AlertChannelConfig is one notification destination.
type AlertChannelConfig struct {
	// Name is the identifier rules reference in their `channels:` list.
	Name string `yaml:"name"`
	// Type selects the transport: discord | slack | ntfy | pushover |
	// telegram | gotify | webhook | exec | mqtt.
	Type string `yaml:"type"`
	// URL: the Discord / Slack incoming-webhook URL, the ntfy topic URL
	// (https://ntfy.sh/<topic>), the Gotify server base URL, the generic
	// webhook endpoint, or the MQTT broker (tcp://host:1883 / ssl://…).
	URL string `yaml:"url"`
	// Token: Pushover application token, Telegram bot token, Gotify
	// application token, ntfy access token, or the bearer token a generic
	// webhook sends as `Authorization: Bearer <token>`.
	Token string `yaml:"token"`
	// User: Pushover user/group key, Telegram chat id, or the MQTT / ntfy
	// basic-auth user name.
	User string `yaml:"user"`
	// Password is the MQTT / ntfy basic-auth password.
	Password string `yaml:"password"`
	// Priority: ntfy 1 (min) … 5 (urgent), Pushover -2 … 2, Gotify 0 … 10.
	// 0 leaves the service default.
	Priority int `yaml:"priority"`
	// Topic is the MQTT topic prefix (default "gophertrunk"): alerts publish
	// to <topic>/alerts/<rule>, mirrored events to <topic>/events/<kind>.
	Topic string `yaml:"topic"`
	// MirrorEvents (mqtt only) publishes EVERY bus event as JSON to
	// <topic>/events/<kind> — the trunk-recorder MQTT-plugin shape — in
	// addition to the rule-driven alerts.
	MirrorEvents bool `yaml:"mirror_events"`
	// Command (exec only) is the program to run per alert. It receives the
	// alert JSON on stdin and GT_ALERT_* environment variables.
	Command string `yaml:"command"`
	// Timeout bounds one delivery attempt (HTTP request / command run).
	// Default 10s.
	Timeout string `yaml:"timeout"`
}

// AlertRuleConfig is one alert rule: WHICH events, under WHAT conditions,
// to WHICH channels, saying WHAT.
type AlertRuleConfig struct {
	// Name labels the rule in logs, the API and the delivered message.
	Name string `yaml:"name"`
	// Disabled keeps the rule in the file but never fires it.
	Disabled bool `yaml:"disabled"`
	// On lists the event kinds the rule watches: call.start, call.end,
	// call.complete (a finished recording — the only kind that can attach
	// audio), grant, tone.alert, cc.locked, cc.lost, affiliation,
	// registration, patch, call.encryption, talker.alias, location, and
	// the P25 unit signalling unit.status, unit.message, call.alert,
	// unit.ack, unit.queued, unit.deny, unit.function (radio check /
	// inhibit), unit.monitor. Empty = call.start.
	On []string `yaml:"on"`
	// Systems restricts to these trunking-system names (empty = any).
	Systems []string `yaml:"systems"`
	// Talkgroups restricts to these talkgroup IDs (empty = any). A call on a
	// patched supergroup matches when any member is listed.
	Talkgroups []uint32 `yaml:"talkgroups"`
	// Radios restricts to these source radio IDs (empty = any).
	Radios []uint32 `yaml:"radios"`
	// Emergency, when true, fires only for emergency-flagged calls / grants.
	Emergency bool `yaml:"emergency"`
	// Encrypted: "" = any, "only" = encrypted calls only, "exclude" = clear
	// calls only.
	Encrypted string `yaml:"encrypted"`
	// ToneProfiles restricts tone.alert events to these tone-out profile
	// names (empty = any profile).
	ToneProfiles []string `yaml:"tone_profiles"`
	// MinDurationMs drops call.end / call.complete events shorter than this
	// (squelch crackle, failed decodes). 0 = any length.
	MinDurationMs int `yaml:"min_duration_ms"`
	// Cooldown suppresses repeat firings of this rule for the same
	// (system, talkgroup/profile) key within the window, e.g. "30s", "5m".
	// Empty/0 = fire on every match.
	Cooldown string `yaml:"cooldown"`
	// Keywords, when set, fires only when the event's text — a transcript
	// (call.transcript), a talker alias, a deny reason — contains one of the
	// words (case-insensitive). The scanner-app "alert me when they say
	// 'shots fired'" feature, on top of the transcription backend.
	Keywords []string `yaml:"keywords"`
	// Channels names the channels to deliver to (required).
	Channels []string `yaml:"channels"`
	// Message is an optional Go text/template rendered per event; empty uses
	// a built-in per-kind message. Fields: {{.Kind}} {{.System}}
	// {{.Protocol}} {{.Talkgroup}} {{.TalkgroupAlpha}} {{.Source}}
	// {{.SourceAlpha}} {{.FrequencyMHz}} {{.Emergency}} {{.Encrypted}}
	// {{.Algorithm}} {{.Individual}} {{.DurationSeconds}} {{.EndReason}}
	// {{.ToneProfile}} {{.ToneAlpha}} {{.Device}} {{.At}}.
	Message string `yaml:"message"`
	// AttachAudio (call.complete rules only) attaches the finished recording
	// to channels that can carry a file (discord, telegram, webhook).
	AttachAudio bool `yaml:"attach_audio"`
}

// AlertChannelTypes lists the accepted channel types.
var AlertChannelTypes = []string{"discord", "slack", "ntfy", "pushover", "telegram", "gotify", "webhook", "exec", "mqtt"}

// AlertRuleEventKinds lists the event kinds a rule may watch.
var AlertRuleEventKinds = []string{
	"call.start", "call.end", "call.complete", "grant", "tone.alert",
	"cc.locked", "cc.lost", "affiliation", "registration", "patch",
	"call.encryption", "talker.alias", "location",
	"unit.status", "unit.message", "call.alert", "unit.ack", "unit.queued",
	"unit.deny", "unit.function", "unit.monitor", "call.transcript",
}

// NormalizedType returns the lower-cased, trimmed channel type.
func (c AlertChannelConfig) NormalizedType() string {
	return strings.ToLower(strings.TrimSpace(c.Type))
}

// TimeoutDuration parses Timeout, defaulting to 10 s.
func (c AlertChannelConfig) TimeoutDuration() time.Duration {
	if d, err := time.ParseDuration(strings.TrimSpace(c.Timeout)); err == nil && d > 0 {
		return d
	}
	return 10 * time.Second
}

// CooldownDuration parses Cooldown; 0 when empty or invalid.
func (r AlertRuleConfig) CooldownDuration() time.Duration {
	if d, err := time.ParseDuration(strings.TrimSpace(r.Cooldown)); err == nil && d > 0 {
		return d
	}
	return 0
}

// EventKinds returns the rule's `on` list, lower-cased, defaulting to
// call.start.
func (r AlertRuleConfig) EventKinds() []string {
	if len(r.On) == 0 {
		return []string{"call.start"}
	}
	out := make([]string, 0, len(r.On))
	for _, k := range r.On {
		out = append(out, strings.ToLower(strings.TrimSpace(k)))
	}
	return out
}

// Enabled reports whether the section configures at least one rule with
// at least one channel.
func (a AlertsConfig) Enabled() bool {
	for _, r := range a.Rules {
		if !r.Disabled && len(r.Channels) > 0 {
			return true
		}
	}
	return false
}

func (c Config) validateAlerts() []error {
	var errs []error
	a := c.Alerts
	names := map[string]int{}
	for i, ch := range a.Channels {
		if strings.TrimSpace(ch.Name) == "" {
			errs = append(errs, fmt.Errorf("alerts.channels[%d]: name required", i))
			continue
		}
		if prev, dup := names[ch.Name]; dup {
			errs = append(errs, fmt.Errorf("alerts.channels[%d]: duplicate name %q (also channels[%d])", i, ch.Name, prev))
		}
		names[ch.Name] = i
		t := ch.NormalizedType()
		known := false
		for _, k := range AlertChannelTypes {
			if t == k {
				known = true
			}
		}
		if !known {
			errs = append(errs, fmt.Errorf("alerts.channels[%d] (%s): type %q must be one of %s", i, ch.Name, ch.Type, strings.Join(AlertChannelTypes, "|")))
			continue
		}
		switch t {
		case "discord", "slack", "ntfy", "gotify", "webhook", "mqtt":
			if strings.TrimSpace(ch.URL) == "" {
				errs = append(errs, fmt.Errorf("alerts.channels[%d] (%s): url required for type %s", i, ch.Name, t))
			}
		}
		switch t {
		case "pushover":
			if ch.Token == "" || ch.User == "" {
				errs = append(errs, fmt.Errorf("alerts.channels[%d] (%s): pushover needs token (application token) and user (user key)", i, ch.Name))
			}
			if ch.Priority < -2 || ch.Priority > 2 {
				errs = append(errs, fmt.Errorf("alerts.channels[%d] (%s): pushover priority must be -2..2", i, ch.Name))
			}
		case "telegram":
			if ch.Token == "" || ch.User == "" {
				errs = append(errs, fmt.Errorf("alerts.channels[%d] (%s): telegram needs token (bot token) and user (chat id)", i, ch.Name))
			}
		case "gotify":
			if ch.Token == "" {
				errs = append(errs, fmt.Errorf("alerts.channels[%d] (%s): gotify needs token (application token)", i, ch.Name))
			}
			if ch.Priority < 0 || ch.Priority > 10 {
				errs = append(errs, fmt.Errorf("alerts.channels[%d] (%s): gotify priority must be 0..10", i, ch.Name))
			}
		case "ntfy":
			if ch.Priority < 0 || ch.Priority > 5 {
				errs = append(errs, fmt.Errorf("alerts.channels[%d] (%s): ntfy priority must be 1..5 (0 = default)", i, ch.Name))
			}
		case "exec":
			if strings.TrimSpace(ch.Command) == "" {
				errs = append(errs, fmt.Errorf("alerts.channels[%d] (%s): command required for type exec", i, ch.Name))
			}
		case "mqtt":
			u := strings.TrimSpace(ch.URL)
			if u != "" && !strings.HasPrefix(u, "tcp://") && !strings.HasPrefix(u, "ssl://") && !strings.HasPrefix(u, "tls://") && !strings.HasPrefix(u, "mqtt://") && !strings.HasPrefix(u, "mqtts://") {
				errs = append(errs, fmt.Errorf("alerts.channels[%d] (%s): mqtt url must be tcp://host:port, ssl://host:port, mqtt:// or mqtts://", i, ch.Name))
			}
		}
		if ch.Timeout != "" {
			if _, err := time.ParseDuration(ch.Timeout); err != nil {
				errs = append(errs, fmt.Errorf("alerts.channels[%d] (%s): timeout %q is not a duration", i, ch.Name, ch.Timeout))
			}
		}
	}
	ruleNames := map[string]int{}
	for i, r := range a.Rules {
		label := r.Name
		if label == "" {
			label = fmt.Sprintf("#%d", i)
		}
		if strings.TrimSpace(r.Name) == "" {
			errs = append(errs, fmt.Errorf("alerts.rules[%d]: name required", i))
		} else if prev, dup := ruleNames[r.Name]; dup {
			errs = append(errs, fmt.Errorf("alerts.rules[%d]: duplicate name %q (also rules[%d])", i, r.Name, prev))
		}
		ruleNames[r.Name] = i
		if len(r.Channels) == 0 {
			errs = append(errs, fmt.Errorf("alerts.rules[%d] (%s): channels required (name at least one alerts.channels entry)", i, label))
		}
		for _, cn := range r.Channels {
			if _, ok := names[cn]; !ok {
				errs = append(errs, fmt.Errorf("alerts.rules[%d] (%s): channel %q is not defined under alerts.channels", i, label, cn))
			}
		}
		for _, k := range r.EventKinds() {
			known := false
			for _, kk := range AlertRuleEventKinds {
				if k == kk {
					known = true
				}
			}
			if !known {
				errs = append(errs, fmt.Errorf("alerts.rules[%d] (%s): on: %q must be one of %s", i, label, k, strings.Join(AlertRuleEventKinds, "|")))
			}
		}
		switch strings.ToLower(strings.TrimSpace(r.Encrypted)) {
		case "", "only", "exclude":
		default:
			errs = append(errs, fmt.Errorf("alerts.rules[%d] (%s): encrypted must be \"\", \"only\" or \"exclude\"", i, label))
		}
		if r.Cooldown != "" {
			if _, err := time.ParseDuration(r.Cooldown); err != nil {
				errs = append(errs, fmt.Errorf("alerts.rules[%d] (%s): cooldown %q is not a duration", i, label, r.Cooldown))
			}
		}
		if r.MinDurationMs < 0 {
			errs = append(errs, fmt.Errorf("alerts.rules[%d] (%s): min_duration_ms must not be negative", i, label))
		}
		if r.AttachAudio {
			ok := false
			for _, k := range r.EventKinds() {
				if k == "call.complete" || k == "call.transcript" {
					ok = true
				}
			}
			if !ok {
				errs = append(errs, fmt.Errorf("alerts.rules[%d] (%s): attach_audio needs on: [call.complete] or [call.transcript] (the events that carry a finished recording)", i, label))
			}
		}
	}
	if len(errs) == 0 {
		return nil
	}
	return errs
}
