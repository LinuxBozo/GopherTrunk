package receiver

import (
	"math"
	"math/cmplx"

	"github.com/MattCheramie/GopherTrunk/internal/dsp/filter"
)

// mskDemod is a coherent detector for the ACARS MSK audio (clean-room, from
// MSK theory; acarsdec's demodulator is also coherent, which is why it was
// the reference to beat).
//
// Why coherent. The tone of bit k says whether data bit k EQUALS bit k-1
// (2400 Hz) or differs (1200 Hz), so a receiver that decides tones has to
// integrate them — and one wrong tone decision then inverts EVERY later bit
// of the block (measured on acarsdec's real-air recording: one tone error
// at byte 11 turned the rest of a 90-byte block into its complement). But
// MSK's phase is the same integral: mixed down by the 1800 Hz centre the
// signal's phase moves +π/2 over a "same" bit and −π/2 over a "change" bit,
// so at bit boundary k
//
//	φ_k = φ_0 + (π/2)·(k − 2·n_k),   n_k = number of changes so far,
//
// and after de-rotating by k·π/2 the phase is φ_0 − π·n_k: a BPSK
// constellation whose sign is the data bit (up to the one global sign the
// framer resolves). Reading the phase directly makes a noise hit cost one
// bit, which parity + the block check can repair, instead of a whole block.
//
// Pipeline per audio sample at AudioRateHz (Oversample samples per bit):
//
//	DC block (removes the AM carrier the envelope detector leaves)
//	→ mix by e^{-j2π·1800·t} → complex low-pass (keeps the ±600 Hz
//	  deviation and MSK's main lobe, rejects the 3600 Hz image and the
//	  residual hum, which land at −1500..−1800 Hz)
//	→ timing: a Gardner loop on the ONE-BIT phase difference
//	  x(t) = arg(z(t)·z*(t−T)) / (π/2). Over a bit the phase moves ±π/2, so
//	  x is a triangle wave that peaks (±1, the bit's tone) exactly at the
//	  bit boundaries and crosses zero mid-bit wherever the tone changes —
//	  Gardner's NRZ shape, with the "symbol" instants on the boundaries.
//	  The per-sample frequency was tried first and slipped bits under
//	  noise: a one-bit lag has 4× the signal swing for about the same noise.
//	→ at each bit boundary: de-rotate by k·π/2 and by the tracked carrier
//	  phase θ, slice Re(·), and update θ decision-directed (stable at 0
//	  and π only, like any BPSK loop; both are fine).
type mskDemod struct {
	sps float64

	dcPrev, dcOut float64 // DC blocker state
	osc           complex128
	oscStep       complex128
	oscN          int
	lpf           *filter.FIR
	mixBuf        []complex64
	lpfBuf        []complex64

	// Baseband history for interpolation and the one-bit lag.
	hist [mskHistLen]complex64
	n    int // samples written to hist so far

	// Timing: events alternate between bit boundaries (the decision
	// instants, where x peaks) and mid-bits (where x crosses zero on a
	// tone change). Positions are absolute sample indices.
	nextEvent  float64
	atBoundary bool    // the next event is a boundary
	lastB      float64 // x at the previous boundary
	midX       float64 // x at the mid-bit since then
	tracking   bool    // a block is being framed: use the slow timing gain

	// Carrier.
	k     int     // boundary counter (de-rotation index mod 4)
	theta float64 // tracked phase of the de-rotated constellation
}

// mskHistLen holds a little over two bits of baseband at 8 samples/bit.
const mskHistLen = 32

const (
	mskCentreHz    = (SameHz + ChangeHz) / 2 // 1800
	mskDeviationHz = (SameHz - ChangeHz) / 2 // 600
	// The Gardner loop gain, in bits of timing correction per unit of error
	// (the error is ≈ 4τ/T on a tone change). The pre-key is one steady
	// tone and carries no timing information, so the loop must pull in
	// during the sync characters — mskTimingGainHunt — and once the framer
	// has locked a block it drops to mskTimingGainTrack so noise cannot slip
	// a bit mid-block (a slip shifts every later character; measured as the
	// dominant loss at ~9 dB Eb/N0 with a single gain).
	mskTimingGainHunt  = 0.06
	mskTimingGainTrack = 0.015
	// mskPhaseGain is the decision-directed carrier loop gain (radians per
	// radian of error per bit). The audio "carrier" is the transmitter's
	// 1800 Hz subcarrier seen through an envelope detector, so its drift is
	// only sample-clock ppm — slow — and the gain stays low to ride
	// through noise.
	mskPhaseGain = 0.08
)

func newMSKDemod(rate float64) *mskDemod {
	// Low-pass: keep up to 1300 Hz from centre, Kaiser 60 dB.
	const cutoffHz, n = 1300.0, 127
	beta := 0.1102 * (60 - 8.7)
	d := &mskDemod{
		sps:     rate / acarsBaud,
		lpf:     filter.NewFIR(filter.LowpassKaiser(n, cutoffHz/rate, beta)),
		oscStep: cmplx.Exp(complex(0, -2*math.Pi*mskCentreHz/rate)),
	}
	d.reset()
	return d
}

const acarsBaud = 2400.0

func (d *mskDemod) reset() {
	d.dcPrev, d.dcOut = 0, 0
	d.osc = 1
	d.oscN = 0
	d.lpf.Reset()
	d.hist = [mskHistLen]complex64{}
	d.n = 0
	// The first event needs a full bit of history behind it.
	d.nextEvent = d.sps + 2
	d.atBoundary = true
	d.lastB, d.midX = 0, 0
	d.tracking = false
	d.k = 0
	d.theta = 0
}

// process demodulates audio and calls emit with each soft data value
// (positive and negative are the two data values; magnitude ≈ confidence).
func (d *mskDemod) process(audio []float32, emit func(soft float64)) {
	if cap(d.mixBuf) < len(audio) {
		d.mixBuf = make([]complex64, len(audio))
	}
	mix := d.mixBuf[:len(audio)]
	for i, x := range audio {
		// One-pole DC blocker, corner ≈ 15 Hz at 19.2 kHz.
		y := float64(x) - d.dcPrev + 0.995*d.dcOut
		d.dcPrev, d.dcOut = float64(x), y
		mix[i] = complex64(complex(y, 0) * d.osc)
		d.osc *= d.oscStep
		if d.oscN++; d.oscN == 1024 { // renormalise the oscillator
			d.osc /= complex(cmplx.Abs(d.osc), 0)
			d.oscN = 0
		}
	}
	d.lpfBuf = d.lpf.Process(d.lpfBuf[:0], mix)
	for _, z := range d.lpfBuf {
		d.hist[d.n%mskHistLen] = z
		d.n++
		d.events(emit)
	}
}

// at interpolates the baseband at absolute (fractional) sample position p,
// which must lie within the history.
func (d *mskDemod) at(p float64) complex128 {
	i := int(math.Floor(p))
	f := p - float64(i)
	a := complex128(d.hist[i%mskHistLen])
	b := complex128(d.hist[(i+1)%mskHistLen])
	return a + complex(f, 0)*(b-a)
}

// lagPhase is x(p) = arg(z(p)·z*(p−T)) normalised so a bit's ±π/2 is ±1.
func (d *mskDemod) lagPhase(p float64) (float64, complex128) {
	z := d.at(p)
	zl := d.at(p - d.sps)
	if z == 0 || zl == 0 {
		return 0, z
	}
	return cmplx.Phase(z*cmplx.Conj(zl)) / (math.Pi / 2), z
}

// events runs every timing event whose position the history now covers
// (interpolation needs the sample after it).
func (d *mskDemod) events(emit func(float64)) {
	for d.nextEvent <= float64(d.n-2) {
		x, z := d.lagPhase(d.nextEvent)
		if d.atBoundary {
			// Gardner: the mid-bit sample between two boundaries sits on
			// the tone transition (zero) when timing is right; late
			// sampling puts it on the later bit's side. e ≈ 4τ/T.
			err := (x - d.lastB) * d.midX
			if err > 2 {
				err = 2
			} else if err < -2 {
				err = -2
			}
			g := mskTimingGainHunt
			if d.tracking {
				g = mskTimingGainTrack
			}
			d.nextEvent -= g * err * d.sps
			d.lastB = x
			d.boundary(z, emit)
		} else {
			d.midX = x
		}
		d.atBoundary = !d.atBoundary
		d.nextEvent += d.sps / 2
	}
}

// boundary handles one bit-boundary sample: de-rotate, slice, track phase.
func (d *mskDemod) boundary(z complex128, emit func(float64)) {
	mag := cmplx.Abs(z)
	if mag == 0 {
		emit(0)
		d.k++
		return
	}
	// e^{-j(kπ/2 + θ)}
	rot := cmplx.Exp(complex(0, -(float64(d.k&3)*math.Pi/2 + d.theta)))
	y := z * rot / complex(mag, 0)
	d.k++
	soft := real(y)
	// Decision-directed phase error: the angle of y folded onto the
	// nearest of 0 / π.
	e := imag(y)
	if soft < 0 {
		e = -e
	}
	d.theta += mskPhaseGain * e
	if d.theta > math.Pi {
		d.theta -= 2 * math.Pi
	} else if d.theta < -math.Pi {
		d.theta += 2 * math.Pi
	}
	emit(soft)
}
