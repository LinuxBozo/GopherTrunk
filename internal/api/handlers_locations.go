package api

import (
	"net/http"
	"sort"
	"strconv"

	"github.com/MattCheramie/GopherTrunk/internal/trunking"
)

// handleLocations returns recent geographic fixes for the web map.
// When the location subsystem is not wired (no storage) an empty list
// is returned so the UI renders a stable shape.
func (s *Server) handleLocations(w http.ResponseWriter, r *http.Request) {
	if s.locations == nil {
		writeJSON(w, http.StatusOK, map[string]any{"locations": []LocationFix{}})
		return
	}
	limit := 500
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	fixes, err := s.locations.RecentLocations(limit)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "query locations: "+err.Error())
		return
	}
	if fixes == nil {
		fixes = []LocationFix{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"locations": fixes})
}

// handleAffiliations returns the affiliation tracker's unit-activity
// table — which radio units are currently active on which talkgroups.
// Always 200; an empty list when the tracker is not wired.
func (s *Server) handleAffiliations(w http.ResponseWriter, _ *http.Request) {
	units := []trunking.UnitActivity{}
	if s.affiliations != nil {
		if snap := s.affiliations.Affiliations(); snap != nil {
			units = snap
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"affiliations": units})
}

// handlePatches serves GET /api/v1/patches: the engine's live patch /
// supergroup table — every active Motorola or Harris regroup with its
// member talkgroups. Until now the table was consulted only internally
// (grant attribution) and surfaced as transient `patch` events, so a
// client that connected after the patch was announced could not learn it.
func (s *Server) handlePatches(w http.ResponseWriter, _ *http.Request) {
	patches := []trunking.PatchGroup{}
	if s.patches != nil {
		if snap := s.patches.Patches(); snap != nil {
			patches = snap
		}
	}
	sort.Slice(patches, func(i, j int) bool {
		if patches[i].System != patches[j].System {
			return patches[i].System < patches[j].System
		}
		return patches[i].SuperGroup < patches[j].SuperGroup
	})
	writeJSON(w, http.StatusOK, map[string]any{"patches": patches})
}
