package config

import (
	"fmt"
	"strings"
	"time"
)

// TranscriptionConfig is the `transcription:` section: send each finished
// recording to a speech-to-text server and keep the text with the call.
// The call-sharing ecosystem (Scanner Radio's live transcriptions,
// trunk-recorder's transcription plugins, rdio-scanner front ends) treats
// this as table stakes; GopherTrunk had no speech-to-text anywhere.
//
// The server speaks the OpenAI audio-transcriptions shape (multipart
// `file` + `model` + `language`, JSON `{"text": …}` back) — which is what
// OpenAI, a local whisper.cpp `server` (`/inference`), faster-whisper /
// Speaches, LocalAI and most self-hosted Whisper front ends expose — so one
// client covers all of them. The daemon never runs a model itself.
type TranscriptionConfig struct {
	Enabled bool `yaml:"enabled"`
	// URL is the transcription endpoint, e.g.
	// https://api.openai.com/v1/audio/transcriptions or
	// http://127.0.0.1:8080/inference (whisper.cpp server).
	URL string `yaml:"url"`
	// APIKey, when set, is sent as `Authorization: Bearer <key>`.
	APIKey string `yaml:"api_key"`
	// Model is the `model` form field (OpenAI: whisper-1; ignored by
	// whisper.cpp). Default whisper-1.
	Model string `yaml:"model"`
	// Language is the ISO-639-1 hint (e.g. "en"); empty lets the server detect.
	Language string `yaml:"language"`
	// Prompt is an optional vocabulary hint (unit names, street names, ten
	// codes) the server biases toward; sent as the `prompt` field.
	Prompt string `yaml:"prompt"`
	// Systems / Talkgroups restrict transcription (empty = every call).
	Systems    []string `yaml:"systems"`
	Talkgroups []uint32 `yaml:"talkgroups"`
	// MinDurationMs skips recordings shorter than this (default 1000 ms —
	// a squelch tail transcribes to nonsense and costs the same request).
	MinDurationMs int `yaml:"min_duration_ms"`
	// Workers is the number of concurrent requests (default 1: a local
	// whisper server serialises anyway and a hosted one rate-limits).
	Workers int `yaml:"workers"`
	// Timeout bounds one request (default 60s).
	Timeout string `yaml:"timeout"`
	// UploadFormat: "wav16k" (default) converts the recording to 16 kHz
	// mono 16-bit WAV — the one input every Whisper server accepts;
	// "original" uploads the recording file as recorded (wav / flac).
	UploadFormat string `yaml:"upload_format"`
	// SkipEncrypted skips calls flagged encrypted (default true: ciphertext
	// transcribes to nonsense).
	SkipEncrypted *bool `yaml:"skip_encrypted"`
}

func (t TranscriptionConfig) TimeoutDuration() time.Duration {
	if d, err := time.ParseDuration(strings.TrimSpace(t.Timeout)); err == nil && d > 0 {
		return d
	}
	return 60 * time.Second
}

func (t TranscriptionConfig) ModelOrDefault() string {
	if m := strings.TrimSpace(t.Model); m != "" {
		return m
	}
	return "whisper-1"
}

func (t TranscriptionConfig) MinDuration() time.Duration {
	if t.MinDurationMs < 0 {
		return 0
	}
	if t.MinDurationMs == 0 {
		return time.Second
	}
	return time.Duration(t.MinDurationMs) * time.Millisecond
}

func (t TranscriptionConfig) WorkersOrDefault() int {
	if t.Workers > 0 {
		return t.Workers
	}
	return 1
}

func (t TranscriptionConfig) SkipsEncrypted() bool {
	return t.SkipEncrypted == nil || *t.SkipEncrypted
}

func (c Config) validateTranscription() []error {
	t := c.Transcription
	if !t.Enabled {
		return nil
	}
	var errs []error
	u := strings.TrimSpace(t.URL)
	if u == "" {
		errs = append(errs, fmt.Errorf("transcription.url required when enabled"))
	} else if !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
		errs = append(errs, fmt.Errorf("transcription.url must start with http:// or https://"))
	}
	if t.Timeout != "" {
		if _, err := time.ParseDuration(t.Timeout); err != nil {
			errs = append(errs, fmt.Errorf("transcription.timeout %q is not a duration", t.Timeout))
		}
	}
	if t.Workers < 0 || t.Workers > 16 {
		errs = append(errs, fmt.Errorf("transcription.workers must be 0..16"))
	}
	switch strings.ToLower(strings.TrimSpace(t.UploadFormat)) {
	case "", "wav16k", "original":
	default:
		errs = append(errs, fmt.Errorf("transcription.upload_format must be wav16k or original"))
	}
	return errs
}
