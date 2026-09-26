package demod

import "math"

// AM is a carrier-referenced envelope detector for double-sideband AM (the
// VHF air band, issue #1219). For a transmitted signal C·(1 + m(t)) the
// envelope |z| is proportional to 1 + m(t); the detector tracks the carrier
// level C with a slow one-pole average of the envelope and emits
//
//	|z| / C − 1  =  m(t)
//
// i.e. the modulation itself, with the DC (the carrier) removed and the
// gain set by the carrier — so the output level depends on how deeply the
// transmitter is modulated, never on how strong it arrives. That makes the
// detector its own AGC: the carrier is the reference, which is exactly what
// an FM chain lacks and has to add after the fact.
//
// The carrier tracker's time constant (AMCarrierTau) is long against the
// lowest voice frequency, so voice modulation averages out of it, and short
// enough to follow slow fading. Overmodulation (|z| → 0) reads −1; the
// output is clamped to ±AMClamp so a noise spike on a faded carrier cannot
// produce a full-scale click.
type AM struct {
	alpha   float64
	carrier float64
	primed  bool
}

const (
	// AMCarrierTau is the carrier tracker's time constant. 100 ms is
	// ~30 periods of the lowest voice tone (300 Hz) and still follows
	// flutter from an aircraft's propeller or a moving aircraft.
	AMCarrierTau = 0.1
	// AMClamp bounds the output. Real modulation stays within ±1.
	AMClamp = 1.5
)

// NewAM returns a detector for IQ at rateHz.
func NewAM(rateHz float64) *AM {
	return &AM{alpha: 1 - math.Exp(-1/(AMCarrierTau*rateHz))}
}

// Process demodulates src into dst (grown as needed) and returns it.
func (a *AM) Process(dst []float32, src []complex64) []float32 {
	if cap(dst) < len(src) {
		dst = make([]float32, len(src))
	} else {
		dst = dst[:len(src)]
	}
	for i, s := range src {
		env := math.Hypot(float64(real(s)), float64(imag(s)))
		if !a.primed {
			a.carrier = env
			a.primed = true
		} else {
			a.carrier += a.alpha * (env - a.carrier)
		}
		var m float64
		if a.carrier > 0 {
			m = env/a.carrier - 1
		}
		if m > AMClamp {
			m = AMClamp
		} else if m < -AMClamp {
			m = -AMClamp
		}
		dst[i] = float32(m)
	}
	return dst
}

// Reset forgets the carrier estimate; the next sample re-primes it.
func (a *AM) Reset() {
	a.carrier = 0
	a.primed = false
}
