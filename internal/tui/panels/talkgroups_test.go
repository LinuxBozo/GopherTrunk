package panels

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/MattCheramie/GopherTrunk/internal/tui/client"
	"github.com/MattCheramie/GopherTrunk/internal/tui/state"
)

func TestTalkgroupsPanel_Filter(t *testing.T) {
	p := NewTalkgroups()
	s := &state.SharedState{
		Talkgroups: []client.TalkgroupDTO{
			{ID: 1, AlphaTag: "Dispatch"},
			{ID: 2, AlphaTag: "Tac1"},
			{ID: 3, AlphaTag: "Fire"},
		},
	}
	// First Update populates the table (3 rows).
	_, _ = p.Update(tea.WindowSizeMsg{Width: 120, Height: 30}, s)
	if p.RowCount() != 3 {
		t.Fatalf("initial rows = %d, want 3", p.RowCount())
	}

	// Apply a filter: should narrow to 1 row.
	p.SetFilterValue("disp")
	_, _ = p.Update(tea.WindowSizeMsg{Width: 120, Height: 30}, s)
	if p.RowCount() != 1 {
		t.Errorf("filtered rows = %d, want 1 (matching 'Dispatch')", p.RowCount())
	}

	// Empty filter restores all rows.
	p.SetFilterValue("")
	_, _ = p.Update(tea.WindowSizeMsg{Width: 120, Height: 30}, s)
	if p.RowCount() != 3 {
		t.Errorf("after clearing filter rows = %d, want 3", p.RowCount())
	}
}

// drainWriteRequest runs a panel Cmd and returns the WriteRequest it
// emitted, failing the test when it emitted something else.
func drainWriteRequest(t *testing.T, cmd tea.Cmd) state.WriteRequest {
	t.Helper()
	if cmd == nil {
		t.Fatal("key produced no Cmd")
	}
	msg, ok := cmd().(WriteActionMsg)
	if !ok {
		t.Fatalf("Cmd produced %T, want WriteActionMsg", cmd())
	}
	return msg.Request
}

// TestTalkgroupsPanel_HoldAndAvoidKeys pins the `h` / `a` toggles: on an
// unheld talkgroup `h` emits a hold for that ID, on the held one it emits
// a release; `a` emits a 30-minute avoid, or an unavoid when the
// talkgroup is already in the live avoid list.
func TestTalkgroupsPanel_HoldAndAvoidKeys(t *testing.T) {
	p := NewTalkgroups()
	s := &state.SharedState{
		Talkgroups: []client.TalkgroupDTO{{ID: 101, AlphaTag: "Dispatch"}},
	}
	_, _ = p.Update(tea.WindowSizeMsg{Width: 120, Height: 30}, s)

	hold := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'h'}}
	avoid := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}}

	_, cmd := p.Update(hold, s)
	r := drainWriteRequest(t, cmd)
	if r.Kind != state.WriteKindTalkgroupHold || r.TalkgroupHold == nil || r.TalkgroupHold.ID != 101 {
		t.Fatalf("h on unheld TG = %+v, want hold of 101", r)
	}

	s.Scanner.Hold = &client.TalkgroupHoldDTO{Talkgroup: 101}
	_, cmd = p.Update(hold, s)
	if r := drainWriteRequest(t, cmd); r.Kind != state.WriteKindTalkgroupReleaseHold {
		t.Fatalf("h on the held TG = %+v, want release", r)
	}

	_, cmd = p.Update(avoid, s)
	r = drainWriteRequest(t, cmd)
	if r.Kind != state.WriteKindTalkgroupAvoid || r.TalkgroupAvoid == nil ||
		r.TalkgroupAvoid.ID != 101 || r.TalkgroupAvoid.Minutes != tgAvoidMinutes {
		t.Fatalf("a on an unavoided TG = %+v, want 30 min avoid of 101", r)
	}

	s.Scanner.Avoids = []client.TalkgroupAvoidDTO{{Talkgroup: 101}}
	_, cmd = p.Update(avoid, s)
	r = drainWriteRequest(t, cmd)
	if r.Kind != state.WriteKindTalkgroupUnavoid || r.TalkgroupAvoid == nil || r.TalkgroupAvoid.ID != 101 {
		t.Fatalf("a on an avoided TG = %+v, want unavoid of 101", r)
	}
}
