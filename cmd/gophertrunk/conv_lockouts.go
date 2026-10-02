package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/MattCheramie/GopherTrunk/internal/scanner/conventional"
	"github.com/MattCheramie/GopherTrunk/internal/storage"
)

// convLockoutPersistence adapts the storage.ConvLockoutStore to the
// conventional scanner's lockout hooks: the frequencies to restore at
// construction and the callback that writes every runtime toggle back.
// A nil store (no storage.path) yields nil/nil, which keeps the scanner's
// runtime-only behaviour.
//
// A store error never reaches the scanner: the lockout still applies in
// memory, and the failure is logged, because refusing an operator's
// lockout over a database hiccup is the worse outcome.
func convLockoutPersistence(store *storage.ConvLockoutStore, log *slog.Logger) ([]uint32, func(conventional.Channel, bool)) {
	if store == nil {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var hz []uint32
	rows, err := store.List(ctx)
	if err != nil {
		log.Warn("conv: persisted lockouts not restored", "err", err)
	} else {
		for _, r := range rows {
			hz = append(hz, r.FrequencyHz)
		}
		if len(hz) > 0 {
			log.Info("conv: restored persisted lockouts", "count", len(hz))
		}
	}
	onChange := func(ch conventional.Channel, locked bool) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var err error
		if locked {
			err = store.Set(ctx, ch.FrequencyHz, ch.Label)
		} else {
			err = store.Clear(ctx, ch.FrequencyHz)
		}
		if err != nil {
			log.Warn("conv: lockout not persisted", "freq_hz", ch.FrequencyHz, "locked", locked, "err", err)
		}
	}
	return hz, onChange
}
