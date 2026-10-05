package conventional

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// gainTuner records SetGain and SetCenterFreq in one ordered log, so a
// test can check each channel's gain lands before its tune.
type gainTuner struct {
	fakeTuner
	mu   sync.Mutex
	log  []string
	gain []int
	fail bool
}

func (g *gainTuner) SetCenterFreq(hz uint32) error {
	g.mu.Lock()
	g.log = append(g.log, "tune")
	g.mu.Unlock()
	return g.fakeTuner.SetCenterFreq(hz)
}

func (g *gainTuner) SetGain(tenthDB int) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.fail {
		return errors.New("i2c stall")
	}
	g.log = append(g.log, "gain")
	g.gain = append(g.gain, tenthDB)
	return nil
}

func (g *gainTuner) gains() []int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]int(nil), g.gain...)
}

func runGainScanner(t *testing.T, g *gainTuner, def int, channels []Channel) {
	t.Helper()
	iq := &fakeIQ{chunks: map[uint32][][]complex64{}, tuner: &g.fakeTuner}
	s, err := New(Options{
		Tuner: g, IQ: iq, Engine: &fakeEngine{}, Recorder: fakeRecorder{},
		DeviceSerial:       "CONV-1",
		Channels:           channels,
		MinDwellPerChannel: 10 * time.Millisecond,
		Gain:               g,
		DefaultGainTenthDB: def,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	_ = s.Run(ctx)
}

// TestConvScannerAppliesPerChannelGain: a channel's own gain is written
// before its tune, a channel without one returns the SDR to the device
// default, and an unchanged gain is not rewritten.
func TestConvScannerAppliesPerChannelGain(t *testing.T) {
	g := &gainTuner{}
	runGainScanner(t, g, 280, []Channel{
		{Label: "Loud", FrequencyHz: 120_000_000, GainTenthDB: 400, GainSet: true},
		{Label: "FM", FrequencyHz: 155_000_000},
		{Label: "FM2", FrequencyHz: 156_000_000},
	})
	got := g.gains()
	if len(got) < 4 {
		t.Fatalf("gain writes = %v, want at least two laps of 400/280", got)
	}
	for i, v := range got {
		want := 400
		if i%2 == 1 {
			want = 280
		}
		if v != want {
			t.Fatalf("gain writes = %v, want alternating 400, 280 (FM→FM2 is not rewritten)", got)
		}
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.log[0] != "gain" || g.log[1] != "tune" {
		t.Fatalf("first ops = %v, want the first channel's gain before its tune", g.log[:2])
	}
}

// TestConvScannerLeavesGainAloneWithoutChannelGains: a scan list where no
// channel sets a gain never writes one — the pre-#1239 behaviour.
func TestConvScannerLeavesGainAloneWithoutChannelGains(t *testing.T) {
	g := &gainTuner{}
	runGainScanner(t, g, -1, []Channel{
		{Label: "A", FrequencyHz: 155_000_000},
		{Label: "B", FrequencyHz: 156_000_000},
	})
	if got := g.gains(); len(got) != 0 {
		t.Fatalf("gain writes = %v, want none", got)
	}
}

// TestConvScannerGainFailureKeepsScanning: a gain write that fails is
// logged and the channel is still tuned.
func TestConvScannerGainFailureKeepsScanning(t *testing.T) {
	g := &gainTuner{fail: true}
	runGainScanner(t, g, -1, []Channel{
		{Label: "A", FrequencyHz: 155_000_000, GainTenthDB: 300, GainSet: true},
	})
	if len(g.tuned()) == 0 {
		t.Fatal("a failed gain write stopped the scanner tuning")
	}
}
