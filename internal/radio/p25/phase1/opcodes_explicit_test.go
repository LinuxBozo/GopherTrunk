package phase1

import (
	"testing"

	"github.com/MattCheramie/GopherTrunk/internal/events"
)

// explicitUpdatePayload builds an opcode 0x03 payload from SDRTrunk's
// GroupVoiceChannelGrantUpdateExplicit bit indexes — SERVICE_OPTIONS 16-23,
// RESERVED 24-31, DOWNLINK band 32-35 / channel 36-47, UPLINK band 48-51 /
// channel 52-63, GROUP_ADDRESS 64-79 — through tsbkBits, independently of
// the parser under test.
func explicitUpdatePayload(svc, dlID, dlNum, ulID, ulNum, group uint64) [8]byte {
	return tsbkBits(
		[3]uint64{16, 8, svc},
		[3]uint64{32, 4, dlID}, [3]uint64{36, 12, dlNum},
		[3]uint64{48, 4, ulID}, [3]uint64{52, 12, ulNum},
		[3]uint64{64, 16, group},
	)
}

// TestParseGroupVoiceChannelUpdateExplicitLayout pins opcode 0x03 against
// literal field positions. It used to be parsed with the 0x00 grant layout
// (channel at bits 24-39), which reads the reserved byte as the channel ID.
func TestParseGroupVoiceChannelUpdateExplicitLayout(t *testing.T) {
	got := ParseGroupVoiceChannelUpdateExplicit(explicitUpdatePayload(0x40, 1, 0x2AB, 2, 0x3CD, 0xBEEF))
	want := GroupVoiceChannelUpdateExplicit{
		ServiceOptions:    0x40,
		DownlinkChannelID: 1, DownlinkChannelNumber: 0x2AB,
		UplinkChannelID: 2, UplinkChannelNumber: 0x3CD,
		GroupAddress: 0xBEEF,
	}
	if got != want {
		t.Errorf("ParseGroupVoiceChannelUpdateExplicit = %+v, want %+v", got, want)
	}
}

// TestExplicitUpdateGrantResolvesDownlinkChannel reproduces issue #1242: a
// UHF site (VUHF IDEN_UP, base 450 MHz, 6.25 kHz step) announcing calls with
// explicit-channel updates. Every grant used to land on 450.000 MHz (channel
// ID 0 from the reserved byte, number = the downlink channel's high byte),
// so the voice tuner sat on an empty carrier and each call timed out.
func TestExplicitUpdateGrantResolvesDownlinkChannel(t *testing.T) {
	const nac = 0x780
	ident := TSBK{Opcode: OpIdentifierUpdateVUHF, Payload: AssembleIdentifierUpdateVUHF(IdentifierUpdate{
		ChannelID: 0, BandwidthHz: 12_500, SpacingHz: 6_250, TxOffsetHz: 5_000_000, BaseHz: 450_000_000,
	})}
	// Downlink channel 0-80 → 450_000_000 + 80*6_250 = 450_500_000; the
	// uplink channel 0-880 is the subscribers' +5 MHz side and must not win.
	upd := TSBK{Opcode: OpGroupVoiceChannelUpdateExpl, Payload: explicitUpdatePayload(0x40, 0, 80, 0, 880, 0x0123)}

	bus := events.NewBus(16)
	defer bus.Close()
	sub := bus.Subscribe()
	defer sub.Close()

	cc := New(Options{Bus: bus, SystemName: "S", FrequencyHz: 450_287_500})
	cc.Process(buildLockedStreamWithTSBK(10, nac, DUIDTrunkingSignaling, ident), 0)
	cc.Process(buildLockedStreamWithTSBK(0, nac, DUIDTrunkingSignaling, upd), 1<<20)

	grants := drainGrants(t, sub)
	if len(grants) != 1 {
		t.Fatalf("got %d grants, want 1", len(grants))
	}
	g := grants[0]
	if g.FrequencyHz != 450_500_000 {
		t.Errorf("freq = %d, want 450500000 (downlink channel 0-80)", g.FrequencyHz)
	}
	if g.GroupID != 0x0123 || g.SourceID != 0 {
		t.Errorf("group/source = %#x/%d, want 0x123/0", g.GroupID, g.SourceID)
	}
	if !g.Encrypted {
		t.Errorf("Encrypted = false, want true (service options 0x40)")
	}
}
