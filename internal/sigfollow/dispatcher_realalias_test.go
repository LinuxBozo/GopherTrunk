package sigfollow

import (
	"testing"
	"time"

	"github.com/MattCheramie/GopherTrunk/internal/events"
	p25p2 "github.com/MattCheramie/GopherTrunk/internal/radio/p25/phase2"
	"github.com/MattCheramie/GopherTrunk/internal/trunking"
)

// The three real Motorola FACCH-S alias PDUs from issue #376 (Victorian MMR,
// TG 20208, RADIO:ISSI 781824.356.200062): HEADER 0x91 + two DATA 0x95
// blocks, post-FEC info bytes with the FACCH CRCs dropped — the same
// literal dump the phase2 assembler tests pin. SDRTrunk decodes this radio's
// alias as "CRIO 0062".
var (
	realAliasHeaderMSG = []byte{0x91, 0x90, 0x11, 0x4E, 0xF0, 0x02, 0x01, 0x00, 0x06,
		0xBE, 0xE0, 0x01, 0x64, 0x03, 0x0D, 0x7E, 0x24}
	realAliasData1MSG = []byte{0x95, 0x90, 0x11, 0x01, 0x04,
		0x4F, 0x6F, 0xF2, 0xFA, 0x9A, 0xC3, 0xEC, 0x34, 0x43, 0x2F, 0xA6, 0x3C}
	realAliasData2MSG = []byte{0x95, 0x90, 0x11, 0x02, 0x0C,
		0x81, 0xC3, 0xC5, 0xD9, 0x6A, 0x96, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}
)

// realAliasSuperframes lays the three real alias PDUs onto MAC sub-frames
// of two superframes (voice everywhere else), runs the dibit stream back
// through the superframe decoder, and returns the Superframes exactly as
// the live voice composer / signalling follower hand them to Dispatch.
func realAliasSuperframes(t *testing.T) []p25p2.Superframe {
	t.Helper()
	parse := func(raw []byte) p25p2.MACPDU {
		p, err := p25p2.ParseMACPDU(raw)
		if err != nil {
			t.Fatalf("ParseMACPDU: %v", err)
		}
		return p
	}
	header, data1, data2 := parse(realAliasHeaderMSG), parse(realAliasData1MSG), parse(realAliasData2MSG)
	// Two superframes: (slot 0 header, slot 6 data1), (slot 0 data2).
	placement := [][p25p2.SubframesPerSuperframe]*p25p2.MACPDU{
		{0: &header, 6: &data1},
		{0: &data2},
	}
	dibits := make([]uint8, 50) // sync settling lead-in
	for _, sf := range placement {
		var subs [p25p2.SubframesPerSuperframe][]uint8
		for i := range subs {
			if pdu := sf[i]; pdu != nil {
				subs[i] = p25p2.EncodeMACSubframe(p25p2.SlotTypeMACSignaling, uint8(i),
					*pdu, p25p2.TrellisOn, p25p2.InterleaveOff)
				continue
			}
			payloads := make([][]byte, p25p2.Voice4VFrameCount)
			for j := range payloads {
				payloads[j] = make([]byte, p25p2.VoiceFrameBytes)
			}
			subs[i] = p25p2.EncodeVoiceSubframe(p25p2.SlotTypeVoice4V, uint8(i), payloads)
		}
		dibits = append(dibits, p25p2.EncodeSuperframe(subs)...)
	}
	sfs := p25p2.NewSuperframeDecoder().Process(dibits, 0)
	if len(sfs) == 0 {
		t.Fatal("no superframes decoded from the real-alias stream")
	}
	return sfs
}

// TestDispatcherPublishesRealMotorolaAliasAsReliable is the end-to-end pin
// for #773 on the shared live path: the real #376 FACCH-S PDUs, carried on
// MAC sub-frames through the superframe decoder and the MACDispatcher both
// the voice composer and the signalling follower run, surface on the bus as
// the radio's actual display name — "CRIO 0062" for RID 200062 — flagged
// reliable (not "⚠ decode unreliable"). It fails if the cipher is ever
// re-gated (CipherVerified=false ⇒ Unreliable=true) or the framing drifts.
func TestDispatcherPublishesRealMotorolaAliasAsReliable(t *testing.T) {
	bus := events.NewBus(64)
	sub := bus.Subscribe()
	defer sub.Close()

	d := NewMACDispatcher(MACDispatcherOptions{
		Bus: bus, Log: quietLog(), LogPrefix: "sigfollow",
		System: "MMR", Serial: "tap-0",
	})
	macCfg := p25p2.MACDecodeConfig{Trellis: p25p2.TrellisOn}
	for _, sf := range realAliasSuperframes(t) {
		d.Dispatch(sf, macCfg)
	}

	deadline := time.After(2 * time.Second)
	for {
		select {
		case ev := <-sub.C:
			if ev.Kind != events.KindTalkerAlias {
				continue
			}
			ta, ok := ev.Payload.(trunking.TalkerAlias)
			if !ok {
				t.Fatalf("KindTalkerAlias payload type = %T", ev.Payload)
			}
			if ta.SourceID != 200062 {
				t.Errorf("SourceID = %d, want 200062", ta.SourceID)
			}
			if ta.Alias != "CRIO 0062" {
				t.Errorf("Alias = %q, want %q", ta.Alias, "CRIO 0062")
			}
			if ta.Unreliable {
				t.Error("Unreliable = true; the verified cipher's clean decode must publish as reliable")
			}
			if ta.System != "MMR" || ta.Protocol != "p25-phase2" {
				t.Errorf("System/Protocol = %q/%q, want MMR/p25-phase2", ta.System, ta.Protocol)
			}
			return
		case <-deadline:
			t.Fatal("no KindTalkerAlias published for the real #376 alias PDUs")
		}
	}
}
