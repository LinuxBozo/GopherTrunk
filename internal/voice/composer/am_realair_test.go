package composer

import (
	"encoding/binary"
	"math"
	"os"
	"testing"

	"github.com/MattCheramie/GopherTrunk/internal/dsp"
)

// TestAMChainRealAirTuningErrorKeepsAudio is the #1219 on-air pin for the
// carrier tracking (am_afc.go). The reporter's 126.400 MHz capture (fixture
// shared with the scanner's real-air squelch test) is moved 3315 Hz off the
// channel centre, the tuning error their first captures carried. Before the
// chain tracked the carrier, the ±4.5 kHz channel filter sat on the tuned
// frequency and cut the far sideband: on this slice the old chain lost
// 4.1 dB in 1–3 kHz. The decode must now match the on-centre decode of the
// same transmission there.
func TestAMChainRealAirTuningErrorKeepsAudio(t *testing.T) {
	const rate = 2_400_000
	b, err := os.ReadFile("../../scanner/conventional/testdata/am_kric_126400k_carrier_48k.cs16")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	in := make([]complex64, len(b)/4)
	for i := range in {
		in[i] = complex(
			float32(int16(binary.LittleEndian.Uint16(b[4*i:])))/32768,
			float32(int16(binary.LittleEndian.Uint16(b[4*i+2:])))/32768)
	}
	iq := dsp.NewResampler(50, 1, 16, 8.6).Process(nil, in)

	centred := recordAnalog(t, "am-conv", iq)
	// NCO(+f) moves a line at +f to DC, so NCO(+3315) moves the carrier
	// (≈ −80 Hz as captured) down to ≈ −3.4 kHz.
	offset := recordAnalog(t, "am-conv", dsp.NewNCO(3315, rate).Mix(nil, iq))
	if r := pcmRMS(centred); r < 200 {
		t.Fatalf("on-centre decode rms %.0f: the chain produced no audio from a live carrier", r)
	}
	// Measure the 1–3 kHz band, where the lost sideband shows: the capture's
	// audio is ~84 % hum below 300 Hz (the reporter's hangar AC), which
	// dominates a whole-band correlation and hides a 6 dB loss above 1 kHz.
	r := voiceBandPower(offset) / voiceBandPower(centred)
	t.Logf("3315 Hz off vs on centre: 1-3 kHz power ratio %.2f (%.1f dB), audio correlation %.3f",
		r, 10*math.Log10(r), pcmCorrelation(offset, centred))
	if r < 0.8 || r > 1.25 {
		t.Errorf("1-3 kHz power with a 3.3 kHz tuning error is %.1f dB of the on-centre decode, want within ±1 dB", 10*math.Log10(r))
	}
}

// voiceBandPower is the audio's mean power in 1–3 kHz.
func voiceBandPower(pcm []int16) float64 {
	f := pcmBandFractions(pcm, 8000)
	rms := pcmRMS(pcm)
	return (f[2] + f[3]) * rms * rms
}
