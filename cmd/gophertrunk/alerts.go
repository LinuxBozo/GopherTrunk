package main

import (
	"log/slog"

	"github.com/MattCheramie/GopherTrunk/internal/alerts"
	"github.com/MattCheramie/GopherTrunk/internal/api"
	"github.com/MattCheramie/GopherTrunk/internal/config"
	"github.com/MattCheramie/GopherTrunk/internal/events"
	"github.com/MattCheramie/GopherTrunk/internal/trunking"
)

// buildAlertsManager constructs the alert-rule / notification subsystem
// from the `alerts:` section. Returns (nil, nil) when no rule is enabled,
// so the daemon simply skips it. Subscribes to the bus at construction so
// events published before Run starts are not lost.
//
// rids (nil-safe) resolves a source radio ID to its alias so a message
// template's {{source_alias}} reads "Engine 12" rather than the bare
// number; the RID database is shared across systems, so the system name
// the event carries is not needed for the lookup.
func buildAlertsManager(cfg config.AlertsConfig, bus *events.Bus, rids *trunking.RIDDB, log *slog.Logger) (*alerts.Manager, error) {
	if !cfg.Enabled() {
		return nil, nil
	}
	return alerts.NewManager(alerts.Options{
		Bus:         bus,
		Config:      cfg,
		Log:         log,
		EventJSON:   api.EventJSON,
		SourceAlias: ridAliasResolver(rids),
	})
}

// ridAliasResolver adapts the RID database to alerts.Options.SourceAlias.
// Returns nil when there is no database so the manager skips the lookup.
func ridAliasResolver(rids *trunking.RIDDB) func(system string, id uint32) string {
	if rids == nil {
		return nil
	}
	return func(_ string, id uint32) string {
		if r := rids.Lookup(id); r != nil {
			return r.Alias
		}
		return ""
	}
}
