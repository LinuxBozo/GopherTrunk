package alerts

import (
	"bytes"
	"fmt"
	"strings"
	"time"
)

// Notification is what a channel delivers: a short title, a one-paragraph
// text, the rule that fired, the normalised event, and (call.complete rules
// with attach_audio) a recording to attach.
type Notification struct {
	Rule      string    `json:"rule"`
	Title     string    `json:"title"`
	Text      string    `json:"text"`
	At        time.Time `json:"at"`
	Event     Event     `json:"event"`
	AudioPath string    `json:"audio_path,omitempty"`
}

// Render builds the Notification for e under r: the rule's own template
// when configured, else the built-in per-kind message.
func (r *Rule) Render(e Event) Notification {
	n := Notification{Rule: r.Name, Title: e.Title(), At: e.At, Event: e}
	if r.tmpl != nil {
		var buf bytes.Buffer
		if err := r.tmpl.Execute(&buf, e); err == nil {
			n.Text = strings.TrimSpace(buf.String())
		} else {
			n.Text = defaultMessage(e) + " (template error: " + err.Error() + ")"
		}
	} else {
		n.Text = defaultMessage(e)
	}
	if r.attachAudio && e.Kind == "call.complete" {
		n.AudioPath = e.AudioPath
	}
	return n
}

// defaultMessage is the built-in one-line message per event kind.
func defaultMessage(e Event) string {
	var parts []string
	add := func(s string) {
		if s != "" {
			parts = append(parts, s)
		}
	}
	if e.System != "" {
		add(e.System)
	}
	switch e.Kind {
	case "call.start", "call.end", "call.complete", "grant":
		if e.Individual {
			add("private call to " + e.TalkgroupLabel())
		} else if tl := e.TalkgroupLabel(); tl != "" {
			add("TG " + tl)
		}
		if sl := e.SourceLabel(); sl != "" {
			add("from " + sl)
		}
		if e.Emergency {
			add("EMERGENCY")
		}
		if alg := e.Algorithm(); alg != "" {
			add("encrypted (" + alg + ")")
		}
		if f := e.FrequencyMHz(); f != "" {
			add(f + " MHz")
		}
		if d := e.DurationSeconds(); d != "" {
			add(d + " s")
		}
		if e.Kind == "call.end" && e.EndReason != "" {
			add("ended: " + e.EndReason)
		}
	case "tone.alert":
		name := e.ToneAlpha
		if name == "" {
			name = e.ToneProfile
		}
		add("tone-out matched: " + name)
		if len(e.ToneHz) > 0 {
			hz := make([]string, len(e.ToneHz))
			for i, f := range e.ToneHz {
				hz[i] = fmt.Sprintf("%.1f", f)
			}
			add(strings.Join(hz, " / ") + " Hz")
		}
	case "cc.locked":
		add("control channel locked")
		if f := e.FrequencyMHz(); f != "" {
			add(f + " MHz")
		}
	case "cc.lost":
		add("control channel LOST")
		if f := e.FrequencyMHz(); f != "" {
			add(f + " MHz")
		}
	case "affiliation":
		add(fmt.Sprintf("radio %s affiliated to TG %s", e.SourceLabel(), e.TalkgroupLabel()))
	case "registration":
		add(fmt.Sprintf("radio %s registered", e.SourceLabel()))
	case "patch":
		m := make([]string, len(e.PatchedGroups))
		for i, g := range e.PatchedGroups {
			m[i] = uitoa(g)
		}
		add(fmt.Sprintf("patch %d → [%s]", e.Talkgroup, strings.Join(m, ", ")))
	case "call.encryption":
		add(fmt.Sprintf("TG %s %s key %d", e.TalkgroupLabel(), e.Algorithm(), e.KeyID))
	case "talker.alias":
		add(fmt.Sprintf("radio %d is %q", e.Source, e.Alias))
	case "location":
		add(fmt.Sprintf("radio %s at %.5f, %.5f", e.SourceLabel(), e.Latitude, e.Longitude))
	default:
		add(e.Kind)
	}
	if e.Device != "" && (e.Kind == "tone.alert" || e.Kind == "cc.lost") {
		add("[" + e.Device + "]")
	}
	return strings.Join(parts, " · ")
}
