package main

import (
	"log/slog"

	"github.com/MattCheramie/GopherTrunk/internal/alerts"
	"github.com/MattCheramie/GopherTrunk/internal/api"
	"github.com/MattCheramie/GopherTrunk/internal/config"
	"github.com/MattCheramie/GopherTrunk/internal/events"
)

// buildAlertsManager constructs the alert-rule / notification subsystem
// from the `alerts:` section. Returns (nil, nil) when no rule is enabled,
// so the daemon simply skips it. Subscribes to the bus at construction so
// events published before Run starts are not lost.
func buildAlertsManager(cfg config.AlertsConfig, bus *events.Bus, log *slog.Logger) (*alerts.Manager, error) {
	if !cfg.Enabled() {
		return nil, nil
	}
	return alerts.NewManager(alerts.Options{
		Bus:       bus,
		Config:    cfg,
		Log:       log,
		EventJSON: api.EventJSON,
	})
}
