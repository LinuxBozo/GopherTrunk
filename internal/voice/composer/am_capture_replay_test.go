package composer

import (
	"encoding/binary"
	"math"
	"math/cmplx"
	"os"
	"strconv"
	"testing"

	"github.com/MattCheramie/GopherTrunk/internal/dsp"
)

// TestAMCaptureReplay is the #1219 on-air harness: it replays a conventional
// scanner voice capture (the composer's own voice_iq_debug .cs16, i.e. the
// exact IQ an am-conv chain decoded, channel at DC) through the production AM
// chain and prints where the carrier sat plus the recorded audio's band
// fractions. GT_AM_SHIFT_HZ moves the carrier first (e.g. +3000 simulates a
// channel tuned 3 kHz lower), so one capture A/Bs a tuning error.
//
//	GT_AM_IQ=<capture.cs16> [GT_AM_RATE=2400000] [GT_AM_SHIFT_HZ=0] \
//	  go test ./internal/voice/composer -run TestAMCaptureReplay -v
func TestAMCaptureReplay(t *testing.T) {
	path := os.Getenv("GT_AM_IQ")
	if path == "" {
		t.Skip("GT_AM_IQ not set")
	}
	rate := 2_400_000.0
	if v := os.Getenv("GT_AM_RATE"); v != "" {
		r, err := strconv.ParseFloat(v, 64)
		if err != nil {
			t.Fatal(err)
		}
		rate = r
	}
	if rate != 2_400_000 {
		t.Fatalf("GT_AM_RATE %v: the harness drives the composer at 2.4 MS/s", rate)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	iq := make([]complex64, len(b)/4)
	for i := range iq {
		re := float32(int16(binary.LittleEndian.Uint16(b[4*i:]))) / 32768
		im := float32(int16(binary.LittleEndian.Uint16(b[4*i+2:]))) / 32768
		iq[i] = complex(re, im)
	}
	if v := os.Getenv("GT_AM_SHIFT_HZ"); v != "" {
		s, err := strconv.ParseFloat(v, 64)
		if err != nil {
			t.Fatal(err)
		}
		// NCO shifts +s → DC, so shift by −s to move the carrier UP by s.
		iq = dsp.NewNCO(-s, rate).Mix(nil, iq)
	}
	carrier := carrierHz(iq, rate)
	t.Logf("capture: %d samples (%.2f s), carrier at %+.0f Hz", len(iq), float64(len(iq))/rate, carrier)
	pcm := recordAnalog(t, "am-conv", iq)
	t.Logf("as captured: %d samples, rms %.0f, band fractions 0-300/300-1k/1-2k/2-3k/3-4k = %s",
		len(pcm), pcmRMS(pcm), fmtFractions(pcmBandFractions(pcm, 8000)))
	// The same capture with the carrier moved onto the tuned frequency: the
	// chain's audio must not depend on the tuning error.
	centred := recordAnalog(t, "am-conv", dsp.NewNCO(carrier, rate).Mix(nil, iq))
	t.Logf("recentred:   %d samples, rms %.0f, band fractions 0-300/300-1k/1-2k/2-3k/3-4k = %s",
		len(centred), pcmRMS(centred), fmtFractions(pcmBandFractions(centred, 8000)))
	t.Logf("as-captured vs recentred audio: correlation %.3f", pcmCorrelation(pcm, centred))
}

// pcmCorrelation is the best normalised correlation of a and b over lags of
// ±40 samples (5 ms at 8 kHz; the two runs start at slightly different
// points of the PCM stream).
func pcmCorrelation(a, b []int16) float64 {
	best := 0.0
	for lag := -40; lag <= 40; lag++ {
		var sab, saa, sbb float64
		for i := range a {
			j := i + lag
			if j < 0 || j >= len(b) {
				continue
			}
			x, y := float64(a[i]), float64(b[j])
			sab += x * y
			saa += x * x
			sbb += y * y
		}
		if saa > 0 && sbb > 0 {
			best = math.Max(best, sab/math.Sqrt(saa*sbb))
		}
	}
	return best
}

// carrierHz is the strongest line within ±8 kHz of DC (5 Hz steps) over the
// first 0.25 s, after a boxcar decimation to 48 kHz.
func carrierHz(iq []complex64, rate float64) float64 {
	const d = 50
	n := min(len(iq)/d, 12_000)
	y := make([]complex128, n)
	for i := range y {
		var s complex128
		for k := range d {
			s += complex128(iq[i*d+k])
		}
		y[i] = s
	}
	best, bestF := 0.0, 0.0
	for f := -8000.0; f <= 8000; f += 5 {
		var s complex128
		for i, v := range y {
			s += v * cmplx.Exp(complex(0, -2*math.Pi*f*float64(i)*d/rate))
		}
		if m := cmplx.Abs(s); m > best {
			best, bestF = m, f
		}
	}
	return bestF
}

func pcmRMS(pcm []int16) float64 {
	var s float64
	for _, v := range pcm {
		s += float64(v) * float64(v)
	}
	return math.Sqrt(s / float64(max(len(pcm), 1)))
}

// pcmBandFractions is the Welch-averaged (256-point Hann) share of audio
// power in 0-300, 300-1k, 1-2k, 2-3k and 3-4k Hz.
func pcmBandFractions(pcm []int16, fs float64) [5]float64 {
	const n = 256
	edges := [6]float64{0, 300, 1000, 2000, 3000, fs / 2}
	var bands [5]float64
	for off := 0; off+n <= len(pcm); off += n / 2 {
		for k := 0; k <= n/2; k++ {
			var s complex128
			for i := range n {
				w := 0.5 - 0.5*math.Cos(2*math.Pi*float64(i)/n)
				s += complex(float64(pcm[off+i])*w, 0) * cmplx.Exp(complex(0, -2*math.Pi*float64(k*i)/n))
			}
			f := float64(k) * fs / n
			p := real(s)*real(s) + imag(s)*imag(s)
			for b := range bands {
				if f >= edges[b] && (f < edges[b+1] || b == len(bands)-1) {
					bands[b] += p
					break
				}
			}
		}
	}
	var tot float64
	for _, v := range bands {
		tot += v
	}
	if tot > 0 {
		for i := range bands {
			bands[i] /= tot
		}
	}
	return bands
}

func fmtFractions(f [5]float64) string {
	s := ""
	for i, v := range f {
		if i > 0 {
			s += "/"
		}
		s += strconv.FormatFloat(v, 'f', 3, 64)
	}
	return s
}
