package alerts

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/MattCheramie/GopherTrunk/internal/config"
)

// Channel delivers one Notification.
type Channel interface {
	Name() string
	Type() string
	Send(ctx context.Context, n Notification) error
}

// NewChannel builds the channel cfg describes. hc may be nil (a client
// with the channel's timeout is used).
func NewChannel(cfg config.AlertChannelConfig, hc *http.Client) (Channel, error) {
	if hc == nil {
		hc = &http.Client{Timeout: cfg.TimeoutDuration()}
	}
	base := httpChannel{name: cfg.Name, http: hc, cfg: cfg}
	switch cfg.NormalizedType() {
	case "discord":
		return &discordChannel{base}, nil
	case "slack":
		return &slackChannel{base}, nil
	case "ntfy":
		return &ntfyChannel{base}, nil
	case "pushover":
		return &pushoverChannel{base}, nil
	case "telegram":
		return &telegramChannel{base}, nil
	case "gotify":
		return &gotifyChannel{base}, nil
	case "webhook":
		return &webhookChannel{base}, nil
	case "exec":
		return newExecChannel(cfg)
	case "mqtt":
		return newMQTTChannel(cfg)
	}
	return nil, fmt.Errorf("alerts: unknown channel type %q", cfg.Type)
}

type httpChannel struct {
	name string
	http *http.Client
	cfg  config.AlertChannelConfig
}

func (c httpChannel) Name() string { return c.name }
func (c httpChannel) Type() string { return c.cfg.NormalizedType() }

// do sends req and treats any non-2xx as an error carrying the body's
// first line (the services' error messages are short and useful).
func (c httpChannel) do(req *http.Request) error {
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	line := strings.TrimSpace(strings.SplitN(string(body), "\n", 2)[0])
	return fmt.Errorf("%s: HTTP %d %s", c.Type(), resp.StatusCode, line)
}

func (c httpChannel) postJSON(ctx context.Context, endpoint string, body any, headers map[string]string) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	return c.do(req)
}

// postMultipart sends fields plus one file part (field name fileField)
// read from path.
func (c httpChannel) postMultipart(ctx context.Context, endpoint string, fields map[string]string, fileField, path string, headers map[string]string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for k, v := range fields {
		_ = mw.WriteField(k, v)
	}
	part, err := mw.CreateFormFile(fileField, filepath.Base(path))
	if err != nil {
		return err
	}
	if _, err := io.Copy(part, f); err != nil {
		return err
	}
	if err := mw.Close(); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, &buf)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	return c.do(req)
}

// --- Discord (incoming webhook) ------------------------------------------

type discordChannel struct{ httpChannel }

func (c *discordChannel) Send(ctx context.Context, n Notification) error {
	content := "**" + n.Title + "** — " + n.Text
	if len(content) > 1900 {
		content = content[:1900] + "…"
	}
	payload := map[string]any{"content": content, "username": "GopherTrunk"}
	if n.AudioPath != "" {
		pj, _ := json.Marshal(payload)
		return c.postMultipart(ctx, c.cfg.URL, map[string]string{"payload_json": string(pj)}, "files[0]", n.AudioPath, nil)
	}
	return c.postJSON(ctx, c.cfg.URL, payload, nil)
}

// --- Slack (incoming webhook) --------------------------------------------

type slackChannel struct{ httpChannel }

func (c *slackChannel) Send(ctx context.Context, n Notification) error {
	return c.postJSON(ctx, c.cfg.URL, map[string]any{"text": "*" + n.Title + "* — " + n.Text}, nil)
}

// --- ntfy -----------------------------------------------------------------

type ntfyChannel struct{ httpChannel }

func (c *ntfyChannel) Send(ctx context.Context, n Notification) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.URL, strings.NewReader(n.Text))
	if err != nil {
		return err
	}
	req.Header.Set("Title", n.Title)
	req.Header.Set("Tags", ntfyTags(n.Event))
	if c.cfg.Priority > 0 {
		req.Header.Set("Priority", strconv.Itoa(c.cfg.Priority))
	}
	if c.cfg.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.cfg.Token)
	} else if c.cfg.User != "" {
		req.SetBasicAuth(c.cfg.User, c.cfg.Password)
	}
	return c.do(req)
}

func ntfyTags(e Event) string {
	switch {
	case e.Emergency:
		return "rotating_light"
	case e.Kind == "tone.alert":
		return "fire_engine"
	case e.Kind == "cc.lost":
		return "warning"
	case e.Encrypted:
		return "lock"
	}
	return "radio"
}

// --- Pushover -------------------------------------------------------------

const pushoverEndpoint = "https://api.pushover.net/1/messages.json"

type pushoverChannel struct{ httpChannel }

func (c *pushoverChannel) endpoint() string {
	if c.cfg.URL != "" {
		return c.cfg.URL
	}
	return pushoverEndpoint
}

func (c *pushoverChannel) Send(ctx context.Context, n Notification) error {
	form := url.Values{}
	form.Set("token", c.cfg.Token)
	form.Set("user", c.cfg.User)
	form.Set("title", n.Title)
	form.Set("message", n.Text)
	form.Set("timestamp", strconv.FormatInt(n.At.Unix(), 10))
	if c.cfg.Priority != 0 {
		form.Set("priority", strconv.Itoa(c.cfg.Priority))
		if c.cfg.Priority == 2 {
			// Emergency priority requires retry/expire.
			form.Set("retry", "60")
			form.Set("expire", "600")
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint(), strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return c.do(req)
}

// --- Telegram -------------------------------------------------------------

type telegramChannel struct{ httpChannel }

func (c *telegramChannel) base() string {
	if c.cfg.URL != "" {
		return strings.TrimRight(c.cfg.URL, "/")
	}
	return "https://api.telegram.org"
}

func (c *telegramChannel) Send(ctx context.Context, n Notification) error {
	text := "*" + telegramEscape(n.Title) + "* — " + telegramEscape(n.Text)
	if n.AudioPath != "" {
		return c.postMultipart(ctx, c.base()+"/bot"+c.cfg.Token+"/sendAudio",
			map[string]string{"chat_id": c.cfg.User, "caption": text, "parse_mode": "MarkdownV2"},
			"audio", n.AudioPath, nil)
	}
	return c.postJSON(ctx, c.base()+"/bot"+c.cfg.Token+"/sendMessage", map[string]any{
		"chat_id": c.cfg.User, "text": text, "parse_mode": "MarkdownV2",
	}, nil)
}

// telegramEscape escapes MarkdownV2's reserved characters.
func telegramEscape(s string) string {
	var b strings.Builder
	for _, r := range s {
		if strings.ContainsRune(`_*[]()~`+"`"+`>#+-=|{}.!\`, r) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// --- Gotify ---------------------------------------------------------------

type gotifyChannel struct{ httpChannel }

func (c *gotifyChannel) Send(ctx context.Context, n Notification) error {
	body := map[string]any{"title": n.Title, "message": n.Text}
	if c.cfg.Priority > 0 {
		body["priority"] = c.cfg.Priority
	}
	return c.postJSON(ctx, strings.TrimRight(c.cfg.URL, "/")+"/message", body, map[string]string{"X-Gotify-Key": c.cfg.Token})
}

// --- Generic webhook ------------------------------------------------------

type webhookChannel struct{ httpChannel }

func (c *webhookChannel) headers() map[string]string {
	h := map[string]string{"User-Agent": "GopherTrunk-alerts"}
	if c.cfg.Token != "" {
		h["Authorization"] = "Bearer " + c.cfg.Token
	}
	return h
}

func (c *webhookChannel) Send(ctx context.Context, n Notification) error {
	if n.AudioPath != "" {
		meta, _ := json.Marshal(n)
		return c.postMultipart(ctx, c.cfg.URL, map[string]string{"alert": string(meta)}, "audio", n.AudioPath, c.headers())
	}
	return c.postJSON(ctx, c.cfg.URL, n, c.headers())
}

// ensureAudio clears AudioPath when the file is not readable so the text
// still goes out.
func ensureAudio(n *Notification) {
	if n.AudioPath == "" {
		return
	}
	if st, err := os.Stat(n.AudioPath); err != nil || st.IsDir() || st.Size() == 0 {
		n.AudioPath = ""
	}
}
