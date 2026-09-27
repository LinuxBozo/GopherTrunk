// Package afsk wires the MDC1200 DSP pipeline together: an IQ stream
// from the iqtap broker becomes 1200-baud FFSK audio, then an NRZ bit
// stream, then framed MDC1200 bursts on the events bus. The pipeline
// is:
//
//	IQ chunks (Fs Hz, complex64)
//	  → FM demod (internal/dsp/demod.FM)
//	  → real resampler (internal/dsp/resampler_real) to AudioRateHz
//	  → FFSK tone discriminator (internal/dsp/demod.FFSK,
//	    markHz=1200, spaceHz=1800 — CCIR FFSK)
//	  → Mueller-Müller symbol-timing recovery
//	    (internal/dsp/sync.MuellerMuller, 8 sps → 1 sample/symbol)
//	  → zero-threshold tone decision (1200 Hz or 1800 Hz this bit)
//	  → XOR-precoding decode (1800 Hz = the data bit CHANGED,
//	    1200 Hz = it is the SAME as the previous one)
//	  → mdc1200/receiver.Receiver.Push(data bit)
//	  → events.KindMDC1200Message on the bus
//
// Layout mirrors internal/radio/aprs/afsk and fleetsync/afsk: one
// Receiver per (SDR, MDC1200-frequency) pair, the daemon (or the
// conventional scanner, #1220) pumps IQ into it. The difference from
// APRS is the line code. MDC1200 is XOR-precoded MSK (the reference
// modem's own description): the radio sends one cycle of 1200 Hz when a
// data bit equals the previous one and 1.5 cycles of 1800 Hz when it
// changed, so the DATA stream is the running XOR of the tone decisions —
// not the tone decisions themselves. Until #1220 this front end sliced
// the tones straight into the framer as if the line code were plain NRZ
// (its own tests encoded the same way, the #764/#771 self-consistent
// trap), and the 40-bit sync word could never match a real Motorola
// radio: the reporter's on-air FleetSync decoded through the identical
// DSP chain while MDC1200 "never locked". The tone sense is unambiguous
// (an inverted FM discriminator negates the audio, which does not change
// a tone's frequency); only the running XOR's start state is unknown,
// which complements the whole data stream, and the framer's
// complemented-sync lock absorbs that.
package afsk

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync/atomic"

	"github.com/MattCheramie/GopherTrunk/internal/dsp"
	"github.com/MattCheramie/GopherTrunk/internal/dsp/demod"
	dspsync "github.com/MattCheramie/GopherTrunk/internal/dsp/sync"
	"github.com/MattCheramie/GopherTrunk/internal/events"
	mdcrx "github.com/MattCheramie/GopherTrunk/internal/radio/mdc1200/receiver"
)

// CCIR FFSK tone frequencies — the MDC1200 signaling convention.
const (
	MarkHz  = 1200.0 // binary 1
	SpaceHz = 1800.0 // binary 0
)

// Oversample is the number of FFSK-discriminator samples per MDC1200
// bit fed into the Mueller-Müller timing-recovery loop. 8 matches the
// APRS / POCSAG frontends — enough sub-sample resolution to track the
// symbol clock, cheap at 1200 baud.
const Oversample = 8

// mmGain is the Mueller-Müller loop gain — the same conventional
// AFSK-paired starting point the APRS frontend uses.
const mmGain = 0.05

// BaudHz is the MDC1200 signaling rate (fixed at 1200).
const BaudHz = 1200

// AudioRateHz is the audio rate the FFSK discriminator runs at:
// baud × Oversample = 1200 × 8 = 9600.
const AudioRateHz = BaudHz * Oversample

// Options configures a Receiver.
type Options struct {
	// InputRateHz is the IQ sample rate the broker is feeding at.
	InputRateHz uint32

	// SourceName is stamped on log lines and surfaces in metrics.
	SourceName string

	// Bus is required — bursts publish onto KindMDC1200Message via the
	// mdc1200/receiver orchestrator.
	Bus *events.Bus

	// DropBadCRC, when true, silently drops CRC-failed bursts at the
	// orchestrator. Defaults to false — corrupted bursts publish with
	// CRCOK=false so the web panel can flag marginal signals.
	DropBadCRC bool

	// Serial and FrequencyHz identify this receiver's SDR and channel;
	// both are stamped on every published storage.MDC1200Message (#1220).
	// Optional.
	Serial      string
	FrequencyHz uint32

	// Log is optional; defaults to slog.Default.
	Log *slog.Logger
}

// Receiver runs an MDC1200 FFSK decode pipeline against a stream of IQ
// chunks. One Receiver per (SDR, MDC1200-frequency) pair.
type Receiver struct {
	inputRate uint32
	source    string
	log       *slog.Logger

	fm    *demod.FM
	rsmp  *dsp.RealResampler
	ffsk  *demod.FFSK
	mm    *dspsync.MuellerMuller
	inner *mdcrx.Receiver

	// Per-call scratch buffers reused across Process iterations so the
	// hot path never allocates.
	demodBuf    []float32
	rsmpBuf     []float32
	ffskBuf     []float32
	symBuf      []float32
	data        byte // XOR-precoding state: the last data bit delivered
	samplesSeen atomic.Uint64
	bitsEmitted atomic.Uint64
}

// New constructs a Receiver. Returns an error if opts.Bus is nil or
// InputRateHz is unset.
func New(opts Options) (*Receiver, error) {
	if opts.Bus == nil {
		return nil, errors.New("mdc1200/afsk: Bus is required")
	}
	if opts.InputRateHz == 0 {
		return nil, errors.New("mdc1200/afsk: InputRateHz is required")
	}
	log := opts.Log
	if log == nil {
		log = slog.Default()
	}

	out := uint32(AudioRateHz)
	g := gcd(out, opts.InputRateHz)
	L := int(out / g)
	M := int(opts.InputRateHz / g)
	if L == 0 || M == 0 {
		return nil, fmt.Errorf("mdc1200/afsk: bad resample ratio L=%d M=%d", L, M)
	}

	return &Receiver{
		inputRate: opts.InputRateHz,
		source:    opts.SourceName,
		log:       log,
		fm:        demod.NewFM(),
		rsmp:      dsp.NewRealResampler(L, M, 16, 7.0),
		ffsk:      demod.NewFFSK(float64(AudioRateHz), MarkHz, SpaceHz),
		mm:        dspsync.NewMuellerMuller(float64(Oversample), mmGain),
		inner: mdcrx.New(mdcrx.Options{
			Bus:         opts.Bus,
			DropBadCRC:  opts.DropBadCRC,
			Serial:      opts.Serial,
			FrequencyHz: opts.FrequencyHz,
		}),
	}, nil
}

// Process pumps IQ chunks from in through the decode pipeline until
// ctx cancels or in closes.
func (r *Receiver) Process(ctx context.Context, in <-chan []complex64) error {
	if in == nil {
		return errors.New("mdc1200/afsk: input channel is nil")
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case chunk, ok := <-in:
			if !ok {
				return nil
			}
			r.ProcessIQ(chunk)
		}
	}
}

// ProcessIQ runs one IQ chunk through FM → resample → FFSK
// discrimination → MM symbol-time recovery → tone decision → precoding
// decode → push. Bursts publish onto the bus before it returns.
func (r *Receiver) ProcessIQ(chunk []complex64) {
	r.samplesSeen.Add(uint64(len(chunk)))
	r.demodBuf = r.fm.Process(r.demodBuf, chunk)
	r.processAudio(r.demodBuf)
}

// ProcessAudio runs one chunk of already-discriminated FM audio (real
// samples at InputRateHz — a receiver's discriminator tap or a mono audio
// capture) through the same chain minus the FM demod.
func (r *Receiver) ProcessAudio(chunk []float32) {
	r.samplesSeen.Add(uint64(len(chunk)))
	r.processAudio(chunk)
}

func (r *Receiver) processAudio(audio []float32) {
	r.rsmpBuf = r.rsmp.Process(r.rsmpBuf, audio)
	r.ffskBuf = r.ffsk.Discriminate(r.ffskBuf, r.rsmpBuf)
	r.symBuf = r.mm.Process(r.symBuf, r.ffskBuf)
	for _, s := range r.symBuf {
		r.feedSymbol(s)
	}
}

// feedSymbol decides one recovered symbol's tone at a fixed zero
// threshold, applies the XOR-precoding decode and pushes the resulting
// DATA bit into the framer.
//
// The threshold is zero, not a tracked mean, for the reason measured on
// the FleetSync front end (fleetsync/afsk.feedSymbol): the FFSK stage
// mixes to the tone midpoint and low-passes, so its output is DC-free and
// sign-aligned by construction, and a tracked threshold drifts toward a
// long single-tone run — which is exactly what an MDC1200 leader is (the
// alternating 0x55 leader changes on every bit, so it is one continuous
// 1800 Hz tone right up to the sync word).
//
// The decode: mark (positive, 1200 Hz) means this data bit equals the
// previous one; space (non-positive, 1800 Hz) means it changed. One wrong
// tone decision therefore complements every later bit of the burst — the
// CRC rejects such a burst, as it would any other single error, and the
// reference decoder's own sampler has the same per-bit error structure.
func (r *Receiver) feedSymbol(s float32) {
	if s <= 0 {
		r.data ^= 1
	}
	r.inner.Push(r.data)
	r.bitsEmitted.Add(1)
}

// Busy reports whether a burst is part-way through framing (see
// mdc1200/receiver.Receiver.Busy).
func (r *Receiver) Busy() bool { return r.inner.Busy() }

// Reset clears every stage's state so a retune or stream restart does
// not bleed the previous stream's filter history into the next — the
// framer included, so a burst cut off by the retune is abandoned rather
// than completed from the next channel's bits.
func (r *Receiver) Reset() {
	r.fm.Reset()
	r.rsmp.Reset()
	r.ffsk.Reset()
	r.mm = dspsync.NewMuellerMuller(float64(Oversample), mmGain)
	r.data = 0
	r.inner.Reset()
}

// Inner returns the bit-stream orchestrator the frontend is driving.
// Exposed so the daemon can read Stats() for /metrics.
func (r *Receiver) Inner() *mdcrx.Receiver { return r.inner }

// Stats reports cumulative DSP-frontend counters.
type Stats struct {
	IQSamplesSeen uint64 // raw IQ samples Process has consumed
	BitsEmitted   uint64 // bits handed to the orchestrator
}

func (r *Receiver) Stats() Stats {
	return Stats{
		IQSamplesSeen: r.samplesSeen.Load(),
		BitsEmitted:   r.bitsEmitted.Load(),
	}
}

// gcd computes the greatest common divisor via Euclid's algorithm.
func gcd(a, b uint32) uint32 {
	for b != 0 {
		a, b = b, a%b
	}
	return a
}
