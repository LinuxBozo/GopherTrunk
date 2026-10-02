package main

import (
	"log/slog"

	"github.com/MattCheramie/GopherTrunk/internal/config"
	"github.com/MattCheramie/GopherTrunk/internal/events"
	"github.com/MattCheramie/GopherTrunk/internal/storage"
	"github.com/MattCheramie/GopherTrunk/internal/transcribe"
)

// buildTranscriber constructs the speech-to-text backend from the
// `transcription:` section. Returns (nil, nil) when disabled. Subscribes to
// the bus at construction so recordings finished before Run are not lost.
func buildTranscriber(cfg config.TranscriptionConfig, bus *events.Bus, db *storage.DB, log *slog.Logger) (*transcribe.Manager, error) {
	if !cfg.Enabled {
		return nil, nil
	}
	var store transcribe.Store
	if db != nil {
		store = db
	}
	return transcribe.NewManager(transcribe.Options{Bus: bus, Config: cfg, Store: store, Log: log})
}
