package alerts

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/MattCheramie/GopherTrunk/internal/config"
)

// execChannel runs an operator command per alert — SDRTrunk's "script"
// alias action and trunk-recorder's uploadScript / unitScript hooks. The
// alert JSON is written to stdin and the headline fields are exported as
// GT_ALERT_* environment variables so a shell one-liner needs no JSON
// parser. The command string is split on whitespace (no shell), so wrap
// anything fancier in a script.
type execChannel struct {
	name string
	argv []string
	cfg  config.AlertChannelConfig
}

func newExecChannel(cfg config.AlertChannelConfig) (Channel, error) {
	argv := strings.Fields(cfg.Command)
	if len(argv) == 0 {
		return nil, fmt.Errorf("alerts: exec channel %q has no command", cfg.Name)
	}
	return &execChannel{name: cfg.Name, argv: argv, cfg: cfg}, nil
}

func (c *execChannel) Name() string { return c.name }
func (c *execChannel) Type() string { return "exec" }

func (c *execChannel) Send(ctx context.Context, n Notification) error {
	ctx, cancel := context.WithTimeout(ctx, c.cfg.TimeoutDuration())
	defer cancel()
	cmd := exec.CommandContext(ctx, c.argv[0], c.argv[1:]...)
	payload, err := json.Marshal(n)
	if err != nil {
		return err
	}
	cmd.Stdin = bytes.NewReader(payload)
	e := n.Event
	cmd.Env = append(os.Environ(),
		"GT_ALERT_RULE="+n.Rule,
		"GT_ALERT_TITLE="+n.Title,
		"GT_ALERT_TEXT="+n.Text,
		"GT_ALERT_KIND="+e.Kind,
		"GT_ALERT_SYSTEM="+e.System,
		"GT_ALERT_PROTOCOL="+e.Protocol,
		"GT_ALERT_TALKGROUP="+strconv.FormatUint(uint64(e.Talkgroup), 10),
		"GT_ALERT_TALKGROUP_ALPHA="+e.TalkgroupAlpha,
		"GT_ALERT_SOURCE="+strconv.FormatUint(uint64(e.Source), 10),
		"GT_ALERT_FREQUENCY_HZ="+strconv.FormatUint(uint64(e.FrequencyHz), 10),
		"GT_ALERT_EMERGENCY="+strconv.FormatBool(e.Emergency),
		"GT_ALERT_ENCRYPTED="+strconv.FormatBool(e.Encrypted),
		"GT_ALERT_AUDIO_PATH="+n.AudioPath,
		"GT_ALERT_TONE_PROFILE="+e.ToneProfile,
		"GT_ALERT_AT="+e.At.UTC().Format("2006-01-02T15:04:05Z07:00"),
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	cmd.Stdout = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if len(msg) > 200 {
			msg = msg[:200]
		}
		return fmt.Errorf("exec %s: %w %s", c.argv[0], err, msg)
	}
	return nil
}
