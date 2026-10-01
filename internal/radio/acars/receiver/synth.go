package receiver

import (
	"math"
	"math/cmplx"

	"github.com/MattCheramie/GopherTrunk/internal/radio/acars"
)

// SynthAudio renders wire characters (e.g. acars.EncodeBlock) as ACARS MSK
// audio at rate Hz, amplitude ±1: 2400 Hz for a data bit equal to the
// previous one, 1200 Hz for a change, phase-continuous. The line code is the
// one acarsdec decodes (of the four tone/line-code mappings it decodes only
// this one — the offline experiment behind the receiver's package comment);
// tests use it to build faithful transmissions, not to define the format.
func SynthAudio(chars []byte, rate float64) []float32 {
	bits := acars.Bits(chars)
	spb := rate / acars.BaudHz
	out := make([]float32, 0, int(float64(len(bits))*spb)+1)
	var ph, t float64
	prev := byte(1) // the pre-key is all ones
	for _, b := range bits {
		f := ChangeHz
		if b == prev {
			f = SameHz
		}
		prev = b
		t += spb
		for float64(len(out)) < t {
			out = append(out, float32(math.Sin(ph)))
			ph += 2 * math.Pi * f / rate
		}
	}
	return out
}

// SynthAMIQ amplitude-modulates SynthAudio onto a carrier offsetHz from the
// channel centre with modulation depth depth (0..1), carrier amplitude 1.
func SynthAMIQ(chars []byte, rate, offsetHz, depth float64) []complex64 {
	a := SynthAudio(chars, rate)
	out := make([]complex64, len(a))
	step := cmplx.Exp(complex(0, 2*math.Pi*offsetHz/rate))
	osc := complex(1, 0)
	for i, s := range a {
		out[i] = complex64(complex(1+depth*float64(s), 0) * osc)
		osc *= step
	}
	return out
}
