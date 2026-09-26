package demod

import (
	"math"
	"testing"
)

// amTone builds C·(1 + m·cos(2π f t)) with a carrier offset, as IQ.
func amTone(n int, rate, carrierAmp, m, toneHz, offsetHz float64) []complex64 {
	out := make([]complex64, n)
	for i := range out {
		t := float64(i) / rate
		env := carrierAmp * (1 + m*math.Cos(2*math.Pi*toneHz*t))
		ph := 2 * math.Pi * offsetHz * t
		out[i] = complex(float32(env*math.Cos(ph)), float32(env*math.Sin(ph)))
	}
	return out
}

// The detector recovers the modulation depth and tone, with the carrier
// (DC) removed, at any input level and carrier offset — the carrier is the
// gain reference, so there is no absolute-level dependence (#1219).
func TestAMRecoversModulationIndependentOfLevel(t *testing.T) {
	const rate, n = 48_000.0, 48_000
	for _, amp := range []float64{1e-4, 1e-2, 0.9} {
		for _, off := range []float64{0, 700} {
			a := NewAM(rate)
			out := a.Process(nil, amTone(n, rate, amp, 0.6, 1000, off))
			tail := out[n/2:] // past the carrier tracker's settle
			var mean, peak, corr, energy float64
			for i, v := range tail {
				x := float64(v)
				mean += x
				peak = math.Max(peak, math.Abs(x))
				ref := math.Cos(2 * math.Pi * 1000 * float64(i+n/2) / rate)
				corr += x * ref
				energy += x * x
			}
			mean /= float64(len(tail))
			if math.Abs(mean) > 0.02 {
				t.Errorf("amp %g offset %g: output DC %.3f, want ~0 (carrier removed)", amp, off, mean)
			}
			if peak < 0.55 || peak > 0.65 {
				t.Errorf("amp %g offset %g: output peak %.3f, want ~0.6 (the modulation depth)", amp, off, peak)
			}
			// Normalised correlation with the modulating tone.
			if r := corr / math.Sqrt(energy*float64(len(tail))/2); r < 0.99 {
				t.Errorf("amp %g offset %g: correlation with the 1 kHz tone %.3f, want > 0.99", amp, off, r)
			}
		}
	}
}

// An unmodulated carrier demodulates to silence, and overmodulation is clamped.
func TestAMCarrierIsSilentAndOutputIsBounded(t *testing.T) {
	a := NewAM(48_000)
	out := a.Process(nil, amTone(24_000, 48_000, 0.3, 0, 0, 250))
	for _, v := range out {
		if math.Abs(float64(v)) > 1e-3 {
			t.Fatalf("unmodulated carrier produced %v", v)
		}
	}
	a.Reset()
	burst := append(amTone(4800, 48_000, 1e-3, 0, 0, 0), amTone(10, 48_000, 1, 0, 0, 0)...)
	for _, v := range a.Process(nil, burst) {
		if math.Abs(float64(v)) > AMClamp {
			t.Fatalf("output %v exceeds the clamp", v)
		}
	}
}
