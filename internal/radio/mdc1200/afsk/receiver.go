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
//	  → DC-tracking slicer (NRZ bit on the wire)
//	  → mdc1200/receiver.Receiver.Push(bit)
//	  → events.KindMDC1200Message on the bus
//
// Layout mirrors internal/radio/aprs/afsk: one Receiver per (SDR,
// MDC1200-frequency) pair, the daemon pumps the broker subscription
// into Process. The difference from APRS is the line code: MDC1200 is
// plain NRZ, so there is no NRZI stage — the slicer's bit feeds the
// framer directly — and the FFSK tones are 1200/1800 Hz, not the
// Bell-202 1200/2200 Hz.
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
	meanEMA     float32
	meanReady   bool
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
// discrimination → MM symbol-time recovery → slice → push. Bursts
// publish onto the bus before it returns.
func (r *Receiver) ProcessIQ(chunk []complex64) {
	r.samplesSeen.Add(uint64(len(chunk)))
	r.demodBuf = r.fm.Process(r.demodBuf, chunk)
	r.rsmpBuf = r.rsmp.Process(r.rsmpBuf, r.demodBuf)
	r.ffskBuf = r.ffsk.Discriminate(r.ffskBuf, r.rsmpBuf)
	r.symBuf = r.mm.Process(r.symBuf, r.ffskBuf)
	for _, s := range r.symBuf {
		r.feedSymbol(s)
	}
}

// feedSymbol slices one recovered symbol to an NRZ bit (tracking DC
// bias with a slow EMA) and pushes it into the framer. Unlike APRS
// there is no NRZI decode — MDC1200 is plain NRZ.
func (r *Receiver) feedSymbol(s float32) {
	if !r.meanReady {
		r.meanEMA = s
		r.meanReady = true
	} else {
		r.meanEMA += (s - r.meanEMA) * (1.0 / 64.0)
	}
	var bit byte
	if s > r.meanEMA {
		bit = 1
	}
	r.inner.Push(bit)
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
	r.meanReady = false
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
