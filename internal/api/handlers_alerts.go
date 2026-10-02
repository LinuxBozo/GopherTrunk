package api

import (
	"context"
	"net/http"
	"time"

	"github.com/MattCheramie/GopherTrunk/internal/alerts"
)

// handleAlertsStatus serves GET /api/v1/alerts: the compiled alert rules,
// the notification channels with their delivery counters, and the most
// recent firings. `configured: false` when the daemon has no alerts
// section.
func (s *Server) handleAlertsStatus(w http.ResponseWriter, _ *http.Request) {
	if s.alerts == nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"configured": false,
			"channels":   []alerts.ChannelStatus{},
			"rules":      []alerts.RuleStatus{},
			"recent":     []alerts.Firing{},
		})
		return
	}
	st := s.alerts.Status()
	writeJSON(w, http.StatusOK, map[string]any{
		"configured": true,
		"channels":   st.Channels,
		"rules":      st.Rules,
		"matched":    st.Matched,
		"queued":     st.Queued,
		"dropped":    st.Dropped,
		"recent":     st.Recent,
	})
}

// handleAlertsTest serves POST /api/v1/alerts/test/{channel}: deliver a
// synthetic notification through one channel so an operator can confirm a
// Discord webhook / ntfy topic / MQTT broker is reachable without waiting
// for a matching call.
func (s *Server) handleAlertsTest(w http.ResponseWriter, r *http.Request) {
	if s.alerts == nil {
		s.writeError(w, http.StatusServiceUnavailable, "alerts are not configured")
		return
	}
	name := r.PathValue("channel")
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	if err := s.alerts.Test(ctx, name); err != nil {
		s.writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "channel": name})
}
