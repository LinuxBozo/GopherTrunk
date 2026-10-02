package conventional

import (
	"testing"
	"time"
)

// TestConvScannerRestoresPersistedLockouts pins the restart contract: a
// frequency handed in through Options.LockedOutHz starts locked out (the
// scanner never dwells on it), a frequency that matches no channel is
// ignored, and every runtime toggle reaches Options.OnLockoutChange with
// the channel so the daemon can persist it.
func TestConvScannerRestoresPersistedLockouts(t *testing.T) {
	tuner := &fakeTuner{}
	iq := &fakeIQ{chunks: map[uint32][][]complex64{}, tuner: tuner}
	eng := &fakeEngine{}
	type change struct {
		hz     uint32
		locked bool
	}
	var changes []change
	s, err := New(Options{
		Tuner: tuner, IQ: iq, Engine: eng, Recorder: fakeRecorder{},
		DeviceSerial: "CONV-PERSIST",
		SystemName:   "test",
		Channels: []Channel{
			{Label: "A", FrequencyHz: 100_000_000, SquelchDbFS: -10},
			{Label: "B", FrequencyHz: 200_000_000, SquelchDbFS: -10},
			{Label: "C", FrequencyHz: 300_000_000, SquelchDbFS: -10},
		},
		// B persisted as locked out; 999 MHz is stale (no such channel).
		LockedOutHz: []uint32{200_000_000, 999_000_000},
		OnLockoutChange: func(ch Channel, locked bool) {
			changes = append(changes, change{ch.FrequencyHz, locked})
		},
		MinDwellPerChannel: 20 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	snap := s.Snapshot()
	if !snap.Channels[1].LockedOut {
		t.Fatalf("channel B not restored as locked out: %+v", snap.Channels)
	}
	if snap.Channels[0].LockedOut || snap.Channels[2].LockedOut {
		t.Fatalf("unrelated channels locked out: %+v", snap.Channels)
	}
	// The rotation must never visit B.
	for i := 0; i < 6; i++ {
		idx, _, ok := s.pickNextChannel()
		if !ok {
			t.Fatal("pickNextChannel returned ok=false with two unlocked channels")
		}
		if idx == 1 {
			t.Fatalf("pick %d dwelt on the restored locked-out channel", i)
		}
	}
	if len(changes) != 0 {
		t.Fatalf("restoring lockouts must not re-persist them: %+v", changes)
	}
	if !s.UnlockoutChannel(1) || !s.LockoutChannel(2) {
		t.Fatal("toggle on a valid index returned false")
	}
	want := []change{{200_000_000, false}, {300_000_000, true}}
	if len(changes) != len(want) {
		t.Fatalf("OnLockoutChange calls = %+v, want %+v", changes, want)
	}
	for i := range want {
		if changes[i] != want[i] {
			t.Errorf("change %d = %+v, want %+v", i, changes[i], want[i])
		}
	}
}

// TestConvScannerPriorityInterleave pins the priority-scan rotation: with
// PriorityInterleave=2 and one priority channel among four, the priority
// channel is visited after every two ordinary picks, the ordinary cursor
// keeps its own order, and a locked-out priority channel is skipped.
func TestConvScannerPriorityInterleave(t *testing.T) {
	tuner := &fakeTuner{}
	iq := &fakeIQ{chunks: map[uint32][][]complex64{}, tuner: tuner}
	s, err := New(Options{
		Tuner: tuner, IQ: iq, Engine: &fakeEngine{}, Recorder: fakeRecorder{},
		DeviceSerial: "CONV-PRI",
		SystemName:   "test",
		Channels: []Channel{
			{Label: "A", FrequencyHz: 100_000_000},
			{Label: "P", FrequencyHz: 200_000_000, Priority: 1},
			{Label: "C", FrequencyHz: 300_000_000},
			{Label: "D", FrequencyHz: 400_000_000},
		},
		PriorityInterleave: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	var order []string
	for i := 0; i < 10; i++ {
		_, ch, ok := s.pickNextChannel()
		if !ok {
			t.Fatal("ok=false")
		}
		order = append(order, ch.Label)
	}
	// The cursor's own visit to P counts as a sample, so after A P the
	// clock restarts: C D → P (interleave) → A P(cursor) C D → P ...
	// P is never more than two ordinary channels away.
	got := join(order)
	want := join([]string{"A", "P", "C", "D", "P", "A", "P", "C", "D", "P"})
	if got != want {
		t.Fatalf("rotation = %s, want %s", got, want)
	}

	// Lock the priority channel out: the rotation degenerates to the
	// plain round robin over the rest.
	s.LockoutChannel(1)
	order = order[:0]
	for i := 0; i < 6; i++ {
		_, ch, _ := s.pickNextChannel()
		order = append(order, ch.Label)
	}
	for _, l := range order {
		if l == "P" {
			t.Fatalf("locked-out priority channel visited: %v", order)
		}
	}
}

// TestConvScannerPriorityInterleaveOnlyPriority: when every unlocked
// channel is a priority channel there is nothing to interleave against
// and the rotation stays the plain round robin.
func TestConvScannerPriorityInterleaveOnlyPriority(t *testing.T) {
	tuner := &fakeTuner{}
	iq := &fakeIQ{chunks: map[uint32][][]complex64{}, tuner: tuner}
	s, err := New(Options{
		Tuner: tuner, IQ: iq, Engine: &fakeEngine{}, Recorder: fakeRecorder{},
		DeviceSerial: "CONV-PRI-ONLY",
		Channels: []Channel{
			{Label: "P1", FrequencyHz: 100_000_000, Priority: 1},
			{Label: "P2", FrequencyHz: 200_000_000, Priority: 2},
		},
		PriorityInterleave: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	var order []string
	for i := 0; i < 4; i++ {
		_, ch, _ := s.pickNextChannel()
		order = append(order, ch.Label)
	}
	if join(order) != join([]string{"P1", "P2", "P1", "P2"}) {
		t.Fatalf("rotation = %v, want plain round robin", order)
	}
}

func join(ss []string) string {
	out := ""
	for i, s := range ss {
		if i > 0 {
			out += " "
		}
		out += s
	}
	return out
}
