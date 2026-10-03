package conventional

import (
	"context"
	"math"
	"math/rand"
	"testing"
	"time"
)

const chanPowerTestRate = 2_400_000

// spanChunks builds n chunks of chunkLen samples at chanPowerTestRate
// holding one carrier per (offsetHz, amplitude) pair plus complex white
// noise at noiseDbFS total power across the whole SDR span. The phase runs
// on across chunks, as a real stream's does.
func spanChunks(n, chunkLen int, carriers [][2]float64, noiseDbFS float64, seed int64) [][]complex64 {
	rng := rand.New(rand.NewSource(seed))
	sigma := math.Sqrt(math.Pow(10, noiseDbFS/10) / 2)
	out := make([][]complex64, n)
	k := 0
	for c := range out {
		chunk := make([]complex64, chunkLen)
		for i := range chunk {
			t := float64(k) / chanPowerTestRate
			var re, im float64
			for _, cr := range carriers {
				ph := 2 * math.Pi * cr[0] * t
				re += cr[1] * math.Cos(ph)
				im += cr[1] * math.Sin(ph)
			}
			re += rng.NormFloat64() * sigma
			im += rng.NormFloat64() * sigma
			chunk[i] = complex(float32(re), float32(im))
			k++
		}
		out[c] = chunk
	}
	return out
}

// TestConvScannerSquelchIgnoresCarrierElsewhereInSpan is the #1239
// regression. The FM squelch measured the RMS power of the WHOLE SDR span
// (2.4 MHz on an RTL-SDR), not the channel's: any other carrier in the
// span, or an AGC-driven noise floor, held "carrier present" true on every
// scanned channel. With tone gating that left the CTCSS/DCS detector as the
// only gate (the reporter's "Tone only, not CSQ AND Tone"); without it the
// scanner opened calls on empty channels.
//
// Here the scanned channel is empty (noise 70 dB down) while a carrier
// 500 kHz away sits at -20 dBFS. Whole-span power reads ~-20 dBFS — far
// above the -50 dBFS squelch — but nothing is on the channel, so no call
// may open.
func TestConvScannerSquelchIgnoresCarrierElsewhereInSpan(t *testing.T) {
	tuner := &fakeTuner{}
	const freq = 155_000_000
	iq := &fakeIQ{
		tuner: tuner,
		chunks: map[uint32][][]complex64{
			freq: spanChunks(200, 4096, [][2]float64{{500_000, 0.1}}, -90, 1),
		},
	}
	eng := &fakeEngine{}
	s, err := New(Options{
		Tuner: tuner, IQ: iq, Engine: eng, Recorder: fakeRecorder{},
		DeviceSerial:       "CONV-1",
		SystemName:         "test",
		SampleRateHz:       chanPowerTestRate,
		Channels:           []Channel{{Label: "A", FrequencyHz: freq, SquelchDbFS: -50}},
		MinDwellPerChannel: 30 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	_ = s.Run(ctx)
	if n := eng.startCount(); n != 0 {
		t.Fatalf("squelch opened %d call(s) on an empty channel because of a carrier 500 kHz away", n)
	}
}

// TestConvScannerSquelchOpensOnInChannelCarrier is the no-harm half: a
// modest carrier on the channel (2 kHz off centre, -40 dBFS) still opens a
// -50 dBFS squelch with the same strong off-channel carrier present.
func TestConvScannerSquelchOpensOnInChannelCarrier(t *testing.T) {
	tuner := &fakeTuner{}
	const freq = 155_000_000
	iq := &fakeIQ{
		tuner: tuner,
		chunks: map[uint32][][]complex64{
			freq: spanChunks(200, 4096, [][2]float64{{500_000, 0.1}, {2_000, 0.01}}, -90, 2),
		},
	}
	eng := &fakeEngine{}
	s, err := New(Options{
		Tuner: tuner, IQ: iq, Engine: eng, Recorder: fakeRecorder{},
		DeviceSerial:       "CONV-1",
		SystemName:         "test",
		SampleRateHz:       chanPowerTestRate,
		Channels:           []Channel{{Label: "A", FrequencyHz: freq, SquelchDbFS: -50}},
		MinDwellPerChannel: 30 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	_ = s.Run(ctx)
	if eng.startCount() == 0 {
		t.Fatal("squelch never opened on a -40 dBFS in-channel carrier")
	}
}

// TestChannelPowerMeterCalibration pins the meter's scale: a carrier on
// the channel reads what PowerDbFS reads (so squelch_dbfs thresholds keep
// their meaning for a real signal), and a carrier outside the channel
// filter does not register.
func TestChannelPowerMeterCalibration(t *testing.T) {
	for _, tc := range []struct {
		name     string
		offsetHz float64
		amp      float64
		wantLo   float64
		wantHi   float64
	}{
		{"on-channel -20 dBFS", 1_500, 0.1, -21, -19},
		{"12.5 kHz neighbour", 12_500, 0.1, -200, -50},
		{"500 kHz away", 500_000, 0.1, -200, -70},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newChannelPowerMeter(chanPowerTestRate)
			var got float64
			for _, c := range spanChunks(40, 4096, [][2]float64{{tc.offsetHz, tc.amp}}, -120, 3) {
				got = m.process(c)
			}
			if got < tc.wantLo || got > tc.wantHi {
				t.Fatalf("in-channel power = %.1f dBFS, want %.0f..%.0f", got, tc.wantLo, tc.wantHi)
			}
		})
	}
}

// TestChannelPowerMeterHoldsOnTinyChunk: a chunk too short to yield a
// decimated sample repeats the last reading instead of reading as silence.
func TestChannelPowerMeterHoldsOnTinyChunk(t *testing.T) {
	m := newChannelPowerMeter(chanPowerTestRate)
	var want float64
	for _, c := range spanChunks(20, 4096, [][2]float64{{0, 0.1}}, -120, 4) {
		want = m.process(c)
	}
	if got := m.process(make([]complex64, 3)); got != want {
		t.Fatalf("tiny chunk read %.1f dBFS, want the held %.1f", got, want)
	}
}
