package trunking

import (
	"testing"
	"time"

	"github.com/MattCheramie/GopherTrunk/internal/events"
)

func expectNoFollow(t *testing.T, sub *events.Subscription, what string) {
	t.Helper()
	select {
	case ev := <-sub.C:
		if ev.Kind == events.KindCallStart {
			t.Fatalf("%s: unexpected call.start", what)
		}
	case <-time.After(50 * time.Millisecond):
	}
}

func expectFollow(t *testing.T, sub *events.Subscription, what string) {
	t.Helper()
	deadline := time.After(time.Second)
	for {
		select {
		case ev := <-sub.C:
			if ev.Kind == events.KindCallStart {
				return
			}
		case <-deadline:
			t.Fatalf("%s: no call.start", what)
		}
	}
}

// TestEngineHoldFollowsOnlyTheHeldTalkgroup: with a hold on TG 50 every other
// grant is dropped (emergency excepted), the held TG is followed even in
// list mode with Scan=false, and releasing the hold restores scanning.
func TestEngineHoldFollowsOnlyTheHeldTalkgroup(t *testing.T) {
	e, _, bus, tuners := mkEngine(t, 1)
	defer bus.Close()
	e.talkgroups.Add(&TalkGroup{ID: 50, AlphaTag: "HELD", Scan: false})
	e.talkgroups.Add(&TalkGroup{ID: 51, AlphaTag: "OTHER", Scan: true})
	e.SetScanMode(ScanModeList)
	sub := bus.Subscribe()
	defer sub.Close()

	st := e.Hold("X", 50)
	if h, ok := e.Held(); !ok || h.Talkgroup != 50 || h.System != "X" || st.Since.IsZero() {
		t.Fatalf("Held = %+v,%v", h, ok)
	}
	e.HandleGrant(Grant{System: "X", Protocol: "p25", GroupID: 51, FrequencyHz: 1_000_000})
	expectNoFollow(t, sub, "other TG during hold")
	if got := tuners[0].tuned(); len(got) != 0 {
		t.Fatalf("other TG retuned during hold: %v", got)
	}
	// Held TG with Scan=false in list mode still follows.
	e.HandleGrant(Grant{System: "X", Protocol: "p25", GroupID: 50, FrequencyHz: 2_000_000})
	expectFollow(t, sub, "held TG")
	e.EndCall("A-voice", EndReasonNormal)
	drain(sub)
	// Emergency on another TG still gets through.
	e.HandleGrant(Grant{System: "X", Protocol: "p25", GroupID: 51, FrequencyHz: 3_000_000, Emergency: true})
	expectFollow(t, sub, "emergency during hold")
	e.EndCall("A-voice", EndReasonNormal)
	drain(sub)

	if !e.ReleaseHold() {
		t.Fatal("ReleaseHold reported no hold")
	}
	if _, ok := e.Held(); ok {
		t.Fatal("hold still active after release")
	}
	e.HandleGrant(Grant{System: "X", Protocol: "p25", GroupID: 51, FrequencyHz: 4_000_000})
	expectFollow(t, sub, "other TG after release")
}

// TestEngineAvoidIsATimedLockout: an avoided talkgroup is dropped until its
// deadline, then follows again; Unavoid clears early; Avoids() lists live
// entries only; an avoid on one patched member does not block the supergroup.
func TestEngineAvoidIsATimedLockout(t *testing.T) {
	e, _, bus, _ := mkEngine(t, 1)
	defer bus.Close()
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	e.holdAvoid.now = func() time.Time { return now }
	e.talkgroups.Add(&TalkGroup{ID: 60, AlphaTag: "CHATTY", Scan: true})
	sub := bus.Subscribe()
	defer sub.Close()

	a := e.AvoidTalkgroup("X", 60, 30*time.Minute)
	if !a.Until.Equal(now.Add(30 * time.Minute)) {
		t.Fatalf("avoid until = %v", a.Until)
	}
	if list := e.Avoids(); len(list) != 1 || list[0].Talkgroup != 60 {
		t.Fatalf("Avoids = %+v", list)
	}
	e.HandleGrant(Grant{System: "X", Protocol: "p25", GroupID: 60, FrequencyHz: 1_000_000})
	expectNoFollow(t, sub, "avoided TG")
	// System name matching is case-insensitive.
	e.HandleGrant(Grant{System: "x", Protocol: "p25", GroupID: 60, FrequencyHz: 1_000_000})
	expectNoFollow(t, sub, "avoided TG (lower-case system)")
	// Another system's TG 60 is not avoided.
	e.HandleGrant(Grant{System: "Y", Protocol: "p25", GroupID: 60, FrequencyHz: 1_500_000})
	expectFollow(t, sub, "same TG on another system")
	e.EndCall("A-voice", EndReasonNormal)
	drain(sub)

	// Expiry.
	now = now.Add(31 * time.Minute)
	if list := e.Avoids(); len(list) != 0 {
		t.Fatalf("expired avoid still listed: %+v", list)
	}
	e.HandleGrant(Grant{System: "X", Protocol: "p25", GroupID: 60, FrequencyHz: 2_000_000})
	expectFollow(t, sub, "TG after avoid expired")
	e.EndCall("A-voice", EndReasonNormal)
	drain(sub)

	// Early clear.
	e.AvoidTalkgroup("", 60, time.Hour) // any system
	e.HandleGrant(Grant{System: "Z", Protocol: "p25", GroupID: 60, FrequencyHz: 2_500_000})
	expectNoFollow(t, sub, "any-system avoid")
	if !e.UnavoidTalkgroup("", 60) {
		t.Fatal("Unavoid reported nothing to clear")
	}
	e.HandleGrant(Grant{System: "Z", Protocol: "p25", GroupID: 60, FrequencyHz: 3_000_000})
	expectFollow(t, sub, "TG after unavoid")
	e.EndCall("A-voice", EndReasonNormal)
	drain(sub)

	// A supergroup whose members include a non-avoided TG still follows.
	e.patches.Apply(PatchGroup{SuperGroup: 65000, Members: []uint32{60, 61}})
	e.AvoidTalkgroup("X", 65000, time.Hour)
	e.AvoidTalkgroup("X", 60, time.Hour)
	e.HandleGrant(Grant{System: "X", Protocol: "p25", GroupID: 65000, FrequencyHz: 4_000_000})
	expectFollow(t, sub, "patched supergroup with one clear member")
}

func drain(sub *events.Subscription) {
	for {
		select {
		case <-sub.C:
		case <-time.After(30 * time.Millisecond):
			return
		}
	}
}
