package conventional

import (
	"fmt"
	"log/slog"
	"time"

	"github.com/MattCheramie/GopherTrunk/internal/dsp"
	"github.com/MattCheramie/GopherTrunk/internal/dsp/filter"
)

// Data decoders on a conventional channel (issue #1220).
//
// MDC1200 and FleetSync used to need a whole SDR each (mdc1200.channels /
// fleetsync.channels pin a dongle to one frequency). The scanner already
// holds every channel it monitors, so a channel can instead name the data
// decoders to run on its own IQ: `decoders: [mdc1200, fleetsync]` on a
// scanner.conventional entry. The decoders publish onto the same bus kinds
// and logs as the dedicated-SDR receivers, stamped with this scanner's SDR
// serial and the channel's frequency.
//
// Three things the scanner owns so the decoders don't have to:
//
//   - The sample rate. The scanner's IQ arrives at the SDR's full rate
//     (2.4 MS/s on an RTL-SDR) spanning the whole tuner band. An FM
//     discriminator run on that is dominated by the band's noise, and every
//     rad/sample threshold downstream would read 50x off (the CTCSS lesson,
//     #1184). dataFrontEnd decimates to ~dataRefRateHz behind a channel
//     filter first, so every decoder sees one clean narrowband channel at a
//     rate that does not depend on the SDR.
//   - The dwell. A burst the scanner walks away from mid-frame is lost, so a
//     decoder that has locked sync (DataDecoder.Busy) holds the channel: the
//     scan window is extended (bounded by dataScanHoldMax) and, while
//     dwelling, a busy decoder counts as channel activity so hangtime cannot
//     end the call part-way through the burst.
//   - The retune. Decoder state is Reset on every tune, like the tone
//     detectors, so one channel's filter history never bleeds into the next.
//
// A decoder runs on every chunk that clears the channel's power squelch in
// the scan window — including the chunk that opens the squelch, so a burst
// sent at key-up is seen from its first bit — and on every chunk of the
// dwell. It does NOT depend on the CTCSS/DCS gate: a data burst carries its
// own sync word and block check, so it is published even on a channel whose
// tone gate stays shut.

const (
	// DecoderMDC1200 names the Motorola MDC1200 FFSK decoder.
	DecoderMDC1200 = "mdc1200"
	// DecoderFleetSync names the Kenwood FleetSync FFSK decoder.
	DecoderFleetSync = "fleetsync"
	// DecoderACARS names the VHF air-band ACARS decoder (#1231). It runs
	// its own AM envelope detector on the channel IQ, so it needs an AM
	// channel (mode: am) — config validation enforces that.
	DecoderACARS = "acars"
)

// ValidDecoder reports whether name is a data decoder the scanner knows.
func ValidDecoder(name string) bool {
	switch name {
	case DecoderMDC1200, DecoderFleetSync, DecoderACARS:
		return true
	}
	return false
}

// DataDecoder is one data decoder attached to a conventional channel. It
// consumes the channel-filtered IQ the scanner hands it and publishes what
// it decodes itself (the daemon's decoders publish onto the events bus).
// Called only from the scanner's Run goroutine.
type DataDecoder interface {
	// ProcessIQ feeds one chunk of channel IQ at the rate the factory was
	// given.
	ProcessIQ(iq []complex64)
	// Busy reports whether the decoder has locked a sync word and is
	// part-way through a burst.
	Busy() bool
	// Reset clears DSP state; the scanner calls it on every retune.
	Reset()
}

// DataDecoderFactory builds the decoder named kind for channel ch. rateHz is
// the sample rate of the IQ the decoder will be fed (the output of the
// scanner's data front end, not the SDR rate).
type DataDecoderFactory func(ch Channel, kind string, rateHz float64) (DataDecoder, error)

const (
	// dataRefRateHz is the lowest rate the data front end decimates to.
	// 48 kHz leaves a 12.5/25 kHz FM channel (±8 kHz after the channel
	// filter) with margin, and is the rate the FleetSync real-air slices
	// were verified at.
	dataRefRateHz = 48_000
	// dataFFSKRateHz is the FFSK discriminator rate both decoders resample
	// to (1200 baud × 8). The front end prefers a decimation whose output
	// rate shares a large factor with it, so the decoders' rational
	// resamplers stay small.
	dataFFSKRateHz = 9_600
	// dataMaxResampleL bounds the interpolation factor a decoder's
	// resampler would need (dataFFSKRateHz / gcd(rate, dataFFSKRateHz)).
	dataMaxResampleL = 48

	// dataScanHoldStep is how far the scan window is pushed out each time
	// its deadline arrives with a decoder mid-burst.
	dataScanHoldStep = 50 * time.Millisecond
	// dataScanHoldMax bounds that extension. The longest burst either
	// decoder frames — an MDC1200 double packet or a FleetSync II frame —
	// is ~250 bits ≈ 210 ms at 1200 baud, so this covers a burst that
	// locks right at the deadline, while a false sync lock on noise can
	// only delay the scan by this much.
	dataScanHoldMax = 500 * time.Millisecond
	// acarsScanHoldMax is the hold for a channel running the ACARS
	// decoder: its longest block — 16 pre-key characters, 5 of sync and up
	// to 234 of body + 3 trailing, 8 bits each at 2400 bit/s — is ~0.86 s,
	// longer than any FFSK burst.
	acarsScanHoldMax = time.Second
)

// holdMaxFor is the scan-window hold for a decoder kind.
func holdMaxFor(kind string) time.Duration {
	if kind == DecoderACARS {
		return acarsScanHoldMax
	}
	return dataScanHoldMax
}

// pickDataDecimation returns the integer decimation factor m for an input
// rate sampleHz: the largest m that divides the input rate exactly, keeps
// the output rate at or above dataRefRateHz, and keeps the decoders'
// resample ratio small. 1 when no such m exists (the rate is already low,
// or not an integer).
func pickDataDecimation(sampleHz float64) int {
	in := int64(sampleHz)
	if float64(in) != sampleHz || in < 2*dataRefRateHz {
		return 1
	}
	for m := in / dataRefRateHz; m >= 2; m-- {
		if in%m != 0 {
			continue
		}
		out := in / m
		if dataFFSKRateHz/gcd64(out, dataFFSKRateHz) <= dataMaxResampleL {
			return int(m)
		}
	}
	return 1
}

func gcd64(a, b int64) int64 {
	for b != 0 {
		a, b = b, a%b
	}
	return a
}

// dataFrontEnd conditions the scanner's full-rate IQ into one channel for
// the data decoders: integer decimation behind an anti-alias filter, then a
// ±toneChannelCutoffHz channel filter — the same channel the tone front end
// isolates, but kept as IQ, since each decoder runs its own discriminator.
type dataFrontEnd struct {
	pre        *dsp.Resampler // nil when no decimation
	chanFilter *filter.FIR    // nil when the rate is too low to need one
	scratch    []complex64
	chanBuf    []complex64
	rate       float64
}

func newDataFrontEnd(sampleHz float64) *dataFrontEnd {
	m := pickDataDecimation(sampleHz)
	f := &dataFrontEnd{rate: sampleHz / float64(m)}
	if m > 1 {
		f.pre = dsp.NewResampler(1, m, m*8+1, 8.6)
	}
	if fc := toneChannelCutoffHz / f.rate; fc < 0.45 {
		f.chanFilter = filter.NewFIR(filter.LowpassKaiser(63, fc, 8.6))
	}
	return f
}

// process returns the channel IQ for iq. The returned slice is reused by
// the next call.
func (f *dataFrontEnd) process(iq []complex64) []complex64 {
	if f.pre != nil {
		f.scratch = f.pre.Process(f.scratch, iq)
		iq = f.scratch
	}
	if f.chanFilter != nil {
		f.chanBuf = f.chanFilter.Process(f.chanBuf[:0], iq)
		iq = f.chanBuf
	}
	return iq
}

func (f *dataFrontEnd) reset() {
	if f.pre != nil {
		f.pre.Reset()
	}
	if f.chanFilter != nil {
		f.chanFilter.Reset()
	}
}

// channelData is one channel's data front end plus its decoders. nil for a
// channel with no decoders.
type channelData struct {
	fe      *dataFrontEnd
	decs    []DataDecoder
	holdMax time.Duration // longest scan-window hold any of decs needs
}

func (c *channelData) feed(iq []complex64) {
	x := c.fe.process(iq)
	for _, d := range c.decs {
		d.ProcessIQ(x)
	}
}

func (c *channelData) busy() bool {
	for _, d := range c.decs {
		if d.Busy() {
			return true
		}
	}
	return false
}

func (c *channelData) reset() {
	c.fe.reset()
	for _, d := range c.decs {
		d.Reset()
	}
}

// validateDecoders rejects unknown or repeated decoder names.
func validateDecoders(names []string) error {
	seen := map[string]bool{}
	for _, n := range names {
		if !ValidDecoder(n) {
			return fmt.Errorf("decoders: %q must be %s|%s|%s", n, DecoderMDC1200, DecoderFleetSync, DecoderACARS)
		}
		if seen[n] {
			return fmt.Errorf("decoders: %q listed twice", n)
		}
		seen[n] = true
	}
	return nil
}

// buildChannelData builds ch's decoders, or returns nil when it names none.
// Like buildDetector, a decoder the operator configured but that cannot be
// built is WARNed about rather than silently dropped, and the channel keeps
// scanning without it.
func buildChannelData(ch Channel, sampleHz float64, factory DataDecoderFactory, log *slog.Logger) *channelData {
	if len(ch.Decoders) == 0 {
		return nil
	}
	if factory == nil || sampleHz <= 0 {
		if log != nil {
			log.Warn("conv: data decoders configured but the scanner has no decoder factory or sample rate; decoders disabled",
				"freq_hz", ch.FrequencyHz, "label", ch.Label, "decoders", ch.Decoders, "sample_hz", sampleHz)
		}
		return nil
	}
	fe := newDataFrontEnd(sampleHz)
	c := &channelData{fe: fe}
	for _, kind := range ch.Decoders {
		d, err := factory(ch, kind, fe.rate)
		if err != nil || d == nil {
			if log != nil {
				log.Warn("conv: data decoder failed to initialise; channel scans without it",
					"freq_hz", ch.FrequencyHz, "label", ch.Label, "decoder", kind,
					"rate_hz", fe.rate, "err", err)
			}
			continue
		}
		c.decs = append(c.decs, d)
		if h := holdMaxFor(kind); h > c.holdMax {
			c.holdMax = h
		}
	}
	if len(c.decs) == 0 {
		return nil
	}
	return c
}
