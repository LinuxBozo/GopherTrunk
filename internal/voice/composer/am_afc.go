package composer

import (
	"math"
	"sort"

	"github.com/MattCheramie/GopherTrunk/internal/dsp"
	"github.com/MattCheramie/GopherTrunk/internal/dsp/fft"
	"github.com/MattCheramie/GopherTrunk/internal/dsp/window"
)

// AM carrier tracking (issue #1219).
//
// The AM chain's ±amChannelCutoffHz channel filter is centred on the TUNED
// frequency, so a carrier that sits a few kHz off it — the SDR's ppm error,
// the transmitter's own tolerance, or the operator entering a frequency a
// step off — keeps its carrier inside the filter but loses the far
// sideband: the reporter's first on-air captures (#1219, tuned 123.453 MHz)
// carry the carrier at −3315 Hz, so the lower sideband is cut above ~1.2 kHz
// of audio and the envelope detector sees carrier + one sideband (half the
// audio level, plus distortion). An AM carrier is a strong, clean spectral
// line (it holds at least half the signal power), so the chain finds it and
// mixes it to DC before the channel filter — the same search window the
// scanner's C/N squelch opens on (±amAFCSearchHz), so the squelch and the
// chain agree on what "the channel" is.

const (
	// amAFCBlock is the estimator's FFT length at the ~48 kHz intermediate
	// rate: ~23 Hz bins, a new estimate every ~43 ms.
	amAFCBlock = 2048
	// amAFCSearchHz bounds where the carrier may be found. It matches the
	// scanner's AM squelch window and stays clear of an 8.33 kHz
	// neighbour's carrier.
	amAFCSearchHz = 4_500
	// amAFCMinLineDb is how far the strongest bin must stand over the median
	// of the search window before it steers the mixer: a carrier stands
	// 30–55 dB over it on the #1219 captures, receiver noise ~10 dB at most
	// (the largest of ~390 exponential bins over their median).
	amAFCMinLineDb = 15
	// amAFCSmooth is the one-pole weight of each new estimate once locked.
	amAFCSmooth = 0.3
	// amAFCReportHz is the tracked offset above which the chain tells the
	// operator at call end: well past a TCXO's error, well inside the
	// search window.
	amAFCReportHz = 1_000
)

// amCarrierAFC finds the AM carrier within ±amAFCSearchHz of DC and mixes it
// there. It holds its last estimate while no carrier line is present.
type amCarrierAFC struct {
	rate    float64
	nco     *dsp.NCO
	plan    fft.Plan
	win     []float64
	pending []complex64
	in, out []complex128
	search  []int // bin indices within ±amAFCSearchHz
	mags    []float64
	offHz   float64
	locked  bool
}

func newAMCarrierAFC(rateHz float64) *amCarrierAFC {
	a := &amCarrierAFC{
		rate: rateHz,
		nco:  dsp.NewNCO(0, rateHz),
		plan: fft.New(amAFCBlock),
		win:  window.Hann(amAFCBlock),
		in:   make([]complex128, amAFCBlock),
		out:  make([]complex128, amAFCBlock),
	}
	binHz := rateHz / amAFCBlock
	for k := range amAFCBlock {
		f := float64(k) * binHz
		if k >= amAFCBlock/2 {
			f -= rateHz
		}
		if math.Abs(f) <= amAFCSearchHz {
			a.search = append(a.search, k)
		}
	}
	a.mags = make([]float64, len(a.search))
	return a
}

// OffsetHz is the tracked carrier offset from the tuned frequency, and
// whether a carrier has been found at all.
func (a *amCarrierAFC) OffsetHz() (float64, bool) { return a.offHz, a.locked }

// Process mixes src (carrier at the current estimate → DC) into dst and
// returns it, then folds src into the estimate for the chunks that follow.
// dst may alias src: the raw samples are buffered before the mix.
func (a *amCarrierAFC) Process(dst, src []complex64) []complex64 {
	a.pending = append(a.pending, src...)
	dst = a.nco.Mix(dst, src)
	for len(a.pending) >= amAFCBlock {
		a.estimate(a.pending[:amAFCBlock])
		a.pending = a.pending[amAFCBlock:]
	}
	if cap(a.pending) > 4*amAFCBlock {
		a.pending = append([]complex64(nil), a.pending...)
	}
	return dst
}

func (a *amCarrierAFC) estimate(x []complex64) {
	for i, s := range x {
		a.in[i] = complex(float64(real(s))*a.win[i], float64(imag(s))*a.win[i])
	}
	a.out = a.plan.Forward(a.out, a.in)
	peak, peakIdx := -1.0, 0
	for i, k := range a.search {
		c := a.out[k]
		m := real(c)*real(c) + imag(c)*imag(c)
		a.mags[i] = m
		if m > peak {
			peak, peakIdx = m, i
		}
	}
	sorted := append([]float64(nil), a.mags...)
	sort.Float64s(sorted)
	med := sorted[len(sorted)/2]
	if peak <= 0 || med <= 0 || 10*math.Log10(peak/med) < amAFCMinLineDb {
		return // no carrier line: hold
	}
	// Parabolic interpolation on the log power of the peak and its
	// neighbours (search bins are contiguous in frequency except at the
	// DC wrap, where k and its neighbours are still adjacent mod N).
	k := a.search[peakIdx]
	pow := func(j int) float64 {
		c := a.out[(j+amAFCBlock)%amAFCBlock]
		return math.Log(real(c)*real(c) + imag(c)*imag(c) + 1e-300)
	}
	l, c, r := pow(k-1), pow(k), pow(k+1)
	delta := 0.0
	if d := l - 2*c + r; d < 0 {
		delta = 0.5 * (l - r) / d
	}
	bin := float64(k) + delta
	if k >= amAFCBlock/2 {
		bin -= amAFCBlock
	}
	f := bin * a.rate / amAFCBlock
	if !a.locked {
		a.offHz, a.locked = f, true
	} else {
		a.offHz += amAFCSmooth * (f - a.offHz)
	}
	a.nco.SetOffset(a.offHz, a.rate)
}
