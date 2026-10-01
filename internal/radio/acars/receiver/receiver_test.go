package receiver

import (
	"math/rand"
	"testing"
	"time"

	"github.com/MattCheramie/GopherTrunk/internal/events"
	"github.com/MattCheramie/GopherTrunk/internal/radio/acars"
	"github.com/MattCheramie/GopherTrunk/internal/storage"
)

var testBlock = acars.Block{
	Mode: '2', Address: "N123GT", Ack: 0x15, Label: "H1", BlockID: '5',
	Text: "M01AGT0042#DFB/ALT 37000 SPD 0.82 FUEL 123.4 WIND 270/085",
}

func addNoise(iq []complex64, sigma float64, rng *rand.Rand) {
	for i := range iq {
		iq[i] += complex64(complex(sigma*rng.NormFloat64(), sigma*rng.NormFloat64()))
	}
}

// transmission is a keyed AM carrier, a block, and some trailing carrier,
// padded with noise-only time either side.
func transmission(rate, offsetHz, depth, sigma float64, seed int64, blocks ...acars.Block) []complex64 {
	rng := rand.New(rand.NewSource(seed))
	var iq []complex64
	pad := make([]complex64, int(rate*0.2))
	iq = append(iq, pad...)
	for _, b := range blocks {
		iq = append(iq, SynthAMIQ(acars.EncodeBlock(b), rate, offsetHz, depth)...)
		iq = append(iq, make([]complex64, int(rate*0.05))...)
	}
	iq = append(iq, pad...)
	addNoise(iq, sigma, rng)
	return iq
}

func decodeIQ(t *testing.T, iq []complex64, rate uint32, chunk int) []acars.Message {
	t.Helper()
	var got []acars.Message
	r, err := New(Options{InputRateHz: rate, OnMessage: func(m acars.Message) { got = append(got, m) }})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < len(iq); i += chunk {
		r.ProcessIQ(iq[i:min(i+chunk, len(iq))])
	}
	return got
}

func checkOne(t *testing.T, got []acars.Message) {
	t.Helper()
	ok := 0
	for _, m := range got {
		if m.CRCOK {
			ok++
			if m.Address != "N123GT" || m.FlightID != "GT0042" || m.MsgNo != "M01A" || m.Label != "H1" {
				t.Fatalf("fields: %+v", m)
			}
		}
	}
	if ok != 1 {
		t.Fatalf("%d CRC-valid blocks of %d framed, want 1", ok, len(got))
	}
}

// TestDecodesAMChannelAtScannerRates: the rates the conventional scanner's
// data front end hands its decoders (2.4 MS/s/50 and 2.048 MS/s/40), with a
// carrier several kHz off centre — an envelope detector does not care.
func TestDecodesAMChannelAtScannerRates(t *testing.T) {
	for _, tc := range []struct {
		rate   uint32
		offset float64
	}{{48000, 0}, {48000, 3300}, {51200, -2500}} {
		iq := transmission(float64(tc.rate), tc.offset, 0.6, 0.05, 1, testBlock)
		checkOne(t, decodeIQ(t, iq, tc.rate, 4096))
	}
}

// TestDecodeIsChunkInvariant: the scanner's chunk sizes vary; the decode
// must not depend on where a chunk boundary falls.
func TestDecodeIsChunkInvariant(t *testing.T) {
	iq := transmission(48000, 1000, 0.6, 0.05, 2, testBlock)
	for _, chunk := range []int{1, 7, 333, 4096, len(iq)} {
		checkOne(t, decodeIQ(t, iq, 48000, chunk))
	}
}

// TestDecodesAtLowSNR: a weak AM signal, Eb/N0 ≈ 10 dB with the noise
// spread over the whole 48 kHz (Eb = (depth²/2)/2400, N0 = 2σ²/48000). A
// sweep of 40 seeds per level measured 40/40 at 11.4 dB, 37/40 at 8.9 dB,
// 29/40 at 7 dB and the cliff at ~5.5 dB, where wideband noise pushes the
// AM envelope detector into its threshold region.
func TestDecodesAtLowSNR(t *testing.T) {
	iq := transmission(48000, 2000, 0.5, 0.35, 3, testBlock)
	checkOne(t, decodeIQ(t, iq, 48000, 4096))
}

// TestNoiseOnlyFramesNothing: a squelch-open channel with no ACARS on it
// (noise, or an unmodulated carrier) must not produce a valid block.
func TestNoiseOnlyFramesNothing(t *testing.T) {
	rng := rand.New(rand.NewSource(9))
	iq := make([]complex64, 48000*10)
	addNoise(iq, 0.3, rng)
	for i := range iq[:len(iq)/2] {
		iq[i] += 1 // half of it under a carrier
	}
	for _, m := range decodeIQ(t, iq, 48000, 4096) {
		if m.CRCOK {
			t.Fatalf("noise validated a block: %+v", m)
		}
	}
}

// TestProcessAudioAtRTLFMRate: `rtl_fm -M am -s 12500` audio — the form of
// the #1231 reporter's capture — through ProcessAudio.
func TestProcessAudioAtRTLFMRate(t *testing.T) {
	const rate = 12500
	audio := make([]float32, rate/5)
	audio = append(audio, SynthAudio(acars.EncodeBlock(testBlock), rate)...)
	audio = append(audio, make([]float32, rate/5)...)
	rng := rand.New(rand.NewSource(4))
	for i := range audio {
		audio[i] = 0.3*audio[i] + 0.4 + float32(0.03*rng.NormFloat64()) // AM DC included
	}
	var got []acars.Message
	r, err := New(Options{InputRateHz: rate, OnMessage: func(m acars.Message) { got = append(got, m) }})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < len(audio); i += 1000 {
		r.ProcessAudio(audio[i:min(i+1000, len(audio))])
	}
	checkOne(t, got)
}

func TestBusyHoldsUntilTheBlockEnds(t *testing.T) {
	const rate = 48000
	iq := transmission(rate, 0, 0.6, 0.02, 5, testBlock)
	r, err := New(Options{InputRateHz: rate, OnMessage: func(acars.Message) {}})
	if err != nil {
		t.Fatal(err)
	}
	busySeen := false
	for i := 0; i < len(iq); i += 480 {
		r.ProcessIQ(iq[i:min(i+480, len(iq))])
		busySeen = busySeen || r.Busy()
	}
	if !busySeen || r.Busy() {
		t.Fatalf("busySeen=%v busy at end=%v", busySeen, r.Busy())
	}
	// Reset mid-block abandons it.
	var got []acars.Message
	r2, _ := New(Options{InputRateHz: rate, OnMessage: func(m acars.Message) { got = append(got, m) }})
	half := len(iq) / 2
	r2.ProcessIQ(iq[:half])
	if !r2.Busy() {
		t.Fatal("not busy half way through the block")
	}
	r2.Reset()
	r2.ProcessIQ(iq[half:])
	if len(got) != 0 {
		t.Fatalf("a block cut by Reset was emitted: %+v", got)
	}
}

func TestPublishesOnBus(t *testing.T) {
	bus := events.NewBus(16)
	sub := bus.Subscribe()
	defer sub.Close()
	r, err := New(Options{InputRateHz: 48000, Bus: bus, Serial: "00000001", FrequencyHz: 131550000})
	if err != nil {
		t.Fatal(err)
	}
	r.ProcessIQ(transmission(48000, 0, 0.6, 0.02, 6, testBlock))
	for {
		select {
		case ev := <-sub.C:
			if ev.Kind != events.KindACARSMessage {
				continue
			}
			m, ok := ev.Payload.(storage.ACARSMessage)
			if !ok {
				t.Fatalf("payload %T", ev.Payload)
			}
			if !m.CRCOK || m.Address != "N123GT" || m.FlightID != "GT0042" || m.Label != "H1" ||
				m.Mode != "2" || m.BlockID != "5" || !m.Downlink || m.Ack != "!" ||
				m.Serial != "00000001" || m.FrequencyHz != 131550000 {
				t.Fatalf("payload %+v", m)
			}
			return
		case <-time.After(2 * time.Second):
			t.Fatal("no bus event")
		}
	}
}
