package afsk

import (
	"encoding/binary"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/MattCheramie/GopherTrunk/internal/dsp/demod"
	"github.com/MattCheramie/GopherTrunk/internal/events"
	"github.com/MattCheramie/GopherTrunk/internal/radio/mdc1200"
	"github.com/MattCheramie/GopherTrunk/internal/storage"
)

// These tests hold the front end to the AIR format — XOR-precoded MSK as
// the reference modem sends it (see mdc1200/synth.go) — rather than to its
// own idea of the line code. Every one of them fails against the front end
// as it shipped before #1220 (which sliced the tones as NRZ data and never
// matched a real radio's sync word), and the reference-audio test decodes
// audio the reference ENCODER produced, so it cannot share a wrong layout
// with anything in this repository.

const airRate = 48000

// synthAirIQ FM-modulates a burst's tone sequence at 48 kHz with the
// shared FFSK modulator (mark 1200 Hz = "same bit", space 1800 Hz =
// "changed"), padded with quiet carrier either side.
func synthAirIQ(t *testing.T, tones []byte) []complex64 {
	t.Helper()
	iq := demod.ModulateFFSK(tones, airRate, 1200, MarkHz, SpaceHz)
	quiet := make([]complex64, airRate/10)
	for i := range quiet {
		quiet[i] = 1
	}
	all := append(append(append([]complex64{}, quiet...), iq...), quiet...)
	return all
}

// airBus returns a bus and a drained-on-cleanup subscription.
func airBus(t *testing.T) (*events.Bus, *events.Subscription) {
	t.Helper()
	bus := events.NewBus(256)
	sub := bus.Subscribe()
	t.Cleanup(func() {
		sub.Close()
		bus.Close()
	})
	return bus, sub
}

// drainMDC returns every MDC1200 message the subscription has queued.
func drainMDC(sub *events.Subscription) []storage.MDC1200Message {
	var out []storage.MDC1200Message
	for {
		select {
		case ev, ok := <-sub.C:
			if !ok {
				return out
			}
			if ev.Kind != events.KindMDC1200Message {
				continue
			}
			if m, ok := ev.Payload.(storage.MDC1200Message); ok {
				out = append(out, m)
			}
		case <-time.After(50 * time.Millisecond):
			return out
		}
	}
}

func runAirIQ(t *testing.T, iq []complex64, chunk int) []storage.MDC1200Message {
	t.Helper()
	bus, sub := airBus(t)
	r, err := New(Options{InputRateHz: airRate, Bus: bus})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < len(iq); i += chunk {
		r.ProcessIQ(iq[i:min(i+chunk, len(iq))])
	}
	return drainMDC(sub)
}

// crcValid returns the CRC-valid messages carrying the expected packet.
func crcValid(msgs []storage.MDC1200Message, op, arg uint8, unit uint16) (hits int) {
	for _, m := range msgs {
		if m.CRCOK && m.Op == op && m.Arg == arg && m.UnitID == unit {
			hits++
		}
	}
	return hits
}

func describe(msgs []storage.MDC1200Message) string {
	s := ""
	for _, m := range msgs {
		s += m.Body + "; "
	}
	return s
}

// TestReceiverDecodesAirFormatBurst is the #1220 regression: a PTT-ID
// burst laid out and precoded exactly as the reference encoder does
// (pinned literal-for-literal in mdc1200/synth_test.go) must decode through
// the production IQ chain.
func TestReceiverDecodesAirFormatBurst(t *testing.T) {
	tones := mdc1200.Precode(mdc1200.BurstBits(0x01, 0x80, 0x1234))
	msgs := runAirIQ(t, synthAirIQ(t, tones), 4096)
	if crcValid(msgs, 0x01, 0x80, 0x1234) != 1 {
		t.Fatalf("PTT ID 0x1234 not decoded CRC-valid exactly once; got: %s", describe(msgs))
	}
	if msgs[0].Operation != "PTT ID" {
		t.Errorf("Operation = %q, want PTT ID", msgs[0].Operation)
	}
}

// TestReceiverDecodesReferenceEncoderAudio feeds audio the reference
// encoder itself produced (testdata/README.md) — op 0x01 arg 0x80 unit
// 0x1234 at 48 kHz, 68 % amplitude, no extra preamble — through
// ProcessAudio. This is the pin that does not share any code with the
// synthesiser above.
func TestReceiverDecodesReferenceEncoderAudio(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "mdc1200_ref_01_80_1234_48k.s16"))
	if err != nil {
		t.Fatal(err)
	}
	audio := make([]float32, len(raw)/2)
	for i := range audio {
		audio[i] = float32(int16(binary.LittleEndian.Uint16(raw[i*2:]))) / 32768
	}
	for _, chunk := range []int{4096, 1000, 1} {
		bus, sub := airBus(t)
		r, err := New(Options{InputRateHz: airRate, Bus: bus})
		if err != nil {
			t.Fatal(err)
		}
		for i := 0; i < len(audio); i += chunk {
			r.ProcessAudio(audio[i:min(i+chunk, len(audio))])
		}
		msgs := drainMDC(sub)
		if crcValid(msgs, 0x01, 0x80, 0x1234) != 1 {
			t.Fatalf("chunk %d: reference audio not decoded CRC-valid exactly once; got: %s (front=%+v inner=%+v)",
				chunk, describe(msgs), r.Stats(), r.Inner().Stats())
		}
	}
}

// TestReceiverToneDecisionIsPolarityInvariant: an FM discriminator with the
// opposite sense negates the audio, which leaves every tone at the same
// frequency — the decode must not change. And a stray change-tone before
// a burst complements the running XOR's state, so the second burst here
// arrives with every data bit inverted; the framer's complemented-sync lock
// must recover it.
func TestReceiverToneDecisionIsPolarityInvariant(t *testing.T) {
	burst := mdc1200.Precode(mdc1200.BurstBits(0x63, 0x00, 0xABCD))
	tones := append(append(append([]byte{}, burst...), 1, 1, 0), burst...) // one extra "changed" tone between
	iq := synthAirIQ(t, tones)

	msgs := runAirIQ(t, iq, 4096)
	if crcValid(msgs, 0x63, 0x00, 0xABCD) != 2 {
		t.Fatalf("want both bursts (second with complemented data) decoded; got: %s", describe(msgs))
	}

	neg := make([]complex64, len(iq))
	for i, s := range iq {
		neg[i] = complex(real(s), -imag(s)) // conjugate ⇒ negated discriminator output
	}
	msgs = runAirIQ(t, neg, 4096)
	if crcValid(msgs, 0x63, 0x00, 0xABCD) != 2 {
		t.Fatalf("inverted discriminator sense: want both bursts decoded; got: %s", describe(msgs))
	}
}

func TestReceiverIsChunkInvariant(t *testing.T) {
	iq := synthAirIQ(t, mdc1200.Precode(mdc1200.BurstBits(0x01, 0x80, 0x1234)))
	for _, chunk := range []int{1, 37, 500, 4096, len(iq)} {
		if got := crcValid(runAirIQ(t, iq, chunk), 0x01, 0x80, 0x1234); got != 1 {
			t.Errorf("chunk %d: %d CRC-valid decodes, want 1", chunk, got)
		}
	}
}

// TestReceiverDecodesUnderNoise: ≈12 dB SNR on the IQ, as the FleetSync
// front end is held to.
func TestReceiverDecodesUnderNoise(t *testing.T) {
	rng := rand.New(rand.NewSource(1220))
	iq := synthAirIQ(t, mdc1200.Precode(mdc1200.BurstBits(0x01, 0x80, 0x1234)))
	const sigma = 0.18
	noisy := make([]complex64, len(iq))
	for i, s := range iq {
		noisy[i] = s + complex(float32(rng.NormFloat64()*sigma), float32(rng.NormFloat64()*sigma))
	}
	bus, sub := airBus(t)
	r, err := New(Options{InputRateHz: airRate, Bus: bus, DropBadCRC: true})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < len(noisy); i += 4096 {
		r.ProcessIQ(noisy[i:min(i+4096, len(noisy))])
	}
	if got := crcValid(drainMDC(sub), 0x01, 0x80, 0x1234); got != 1 {
		t.Fatalf("noisy burst: %d CRC-valid decodes, want 1", got)
	}
}

// TestReceiverToleratesCarrierOffset: a tuning error puts a DC term on the
// discriminator output; the FFSK mix + low-pass removes it.
func TestReceiverToleratesCarrierOffset(t *testing.T) {
	iq := synthAirIQ(t, mdc1200.Precode(mdc1200.BurstBits(0x01, 0x80, 0x1234)))
	for _, offHz := range []float64{-600, 400} {
		shifted := make([]complex64, len(iq))
		step := 2 * math.Pi * offHz / airRate
		for i, s := range iq {
			c, sn := math.Cos(step*float64(i)), math.Sin(step*float64(i))
			re, im := float64(real(s)), float64(imag(s))
			shifted[i] = complex(float32(re*c-im*sn), float32(re*sn+im*c))
		}
		if got := crcValid(runAirIQ(t, shifted, 4096), 0x01, 0x80, 0x1234); got != 1 {
			t.Errorf("offset %+.0f Hz: %d CRC-valid decodes, want 1", offHz, got)
		}
	}
}

// TestReceiverDecodesDoublePacketOverAir: a selective call (op 0x35)
// carries a second block; the framer collects both and publishes once.
func TestReceiverDecodesDoublePacketOverAir(t *testing.T) {
	tones := mdc1200.Precode(mdc1200.DoubleBurstBits(0x35, 0x00, 0x0777, [4]byte{0x00, 0x00, 0x12, 0x34}))
	msgs := runAirIQ(t, synthAirIQ(t, tones), 4096)
	if len(msgs) != 1 || !msgs[0].CRCOK || msgs[0].Op != 0x35 || msgs[0].UnitID != 0x0777 {
		t.Fatalf("double packet: got %s (n=%d)", describe(msgs), len(msgs))
	}
}

// TestReceiverResetClearsPrecodingState: after Reset the running XOR
// starts from 0 again, so a burst right after a retune decodes exactly as
// a first one does.
func TestReceiverResetClearsPrecodingState(t *testing.T) {
	iq := synthAirIQ(t, mdc1200.Precode(mdc1200.BurstBits(0x01, 0x80, 0x1234)))
	bus, sub := airBus(t)
	r, err := New(Options{InputRateHz: airRate, Bus: bus})
	if err != nil {
		t.Fatal(err)
	}
	r.ProcessIQ(iq[:len(iq)/3]) // part of a burst, then a retune
	r.Reset()
	r.ProcessIQ(iq)
	if got := crcValid(drainMDC(sub), 0x01, 0x80, 0x1234); got != 1 {
		t.Fatalf("after Reset: %d CRC-valid decodes, want 1", got)
	}
}
