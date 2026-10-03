package conventional

import (
	"log/slog"

	"github.com/MattCheramie/GopherTrunk/internal/dsp"
	"github.com/MattCheramie/GopherTrunk/internal/dsp/filter"
)

// FM channel power squelch (issue #1239).
//
// The scanner's IQ is the SDR's whole span (2.4 MHz on an RTL-SDR), and the
// FM squelch used to be PowerDbFS of that span. Any other carrier in the
// span — or the noise floor an AGC tuner holds near full scale — then read
// as "carrier present" on every channel scanned, so squelch_dbfs never
// closed: an untoned channel opened calls on empty air, and a toned one fell
// back to the CTCSS/DCS detector alone ("Tone only, not CSQ AND Tone").
//
// channelPowerMeter measures the power the CHANNEL carries instead: the same
// decimate-to-~48 kHz + ±toneChannelCutoffHz channel filter the tone
// detectors use, then RMS power. For a carrier on the channel the reading is
// the same as before (its power dominates either way), so existing
// squelch_dbfs thresholds keep their meaning for a real signal; what changes
// is that everything outside the channel stops counting.
type channelPowerMeter struct {
	pre        *dsp.Resampler // nil when the input is already near toneRefRateHz
	chanFilter *filter.FIR    // nil when the rate is too low to need one
	scratch    []complex64
	chanBuf    []complex64
	// level is the last reading. A chunk too short to yield a decimated
	// sample repeats it rather than reading as silence (which would break
	// a dwell's hangtime accounting on a stream of tiny chunks).
	level float64
}

func newChannelPowerMeter(sampleHz float64) *channelPowerMeter {
	var pre *dsp.Resampler
	m := int(sampleHz / toneRefRateHz)
	if m < 2 {
		m = 1
	} else {
		pre = dsp.NewResampler(1, m, m*8+1, 8.6)
	}
	rate := sampleHz / float64(m)
	var chanFilter *filter.FIR
	if fc := toneChannelCutoffHz / rate; fc < 0.45 {
		chanFilter = filter.NewFIR(filter.LowpassKaiser(63, fc, 8.6))
	}
	return &channelPowerMeter{pre: pre, chanFilter: chanFilter, level: PowerDbFS(nil)}
}

// process returns the in-channel power of iq in dBFS (a unit-amplitude tone
// on the channel reads 0 dBFS, as PowerDbFS does).
func (m *channelPowerMeter) process(iq []complex64) float64 {
	if m.pre != nil {
		m.scratch = m.pre.Process(m.scratch[:0], iq)
		iq = m.scratch
	}
	if m.chanFilter != nil {
		m.chanBuf = m.chanFilter.Process(m.chanBuf[:0], iq)
		iq = m.chanBuf
	}
	if len(iq) == 0 {
		return m.level
	}
	m.level = PowerDbFS(iq)
	return m.level
}

// reset clears the filter history so the previous channel's samples do not
// leak into the next channel's first reading.
func (m *channelPowerMeter) reset() {
	if m.pre != nil {
		m.pre.Reset()
	}
	if m.chanFilter != nil {
		m.chanFilter.Reset()
	}
	m.level = PowerDbFS(nil)
}

// buildChannelPowerMeter returns the in-channel power meter for an FM
// channel, or nil for an AM channel (which squelches on carrier-to-noise)
// or when the scanner has no sample rate — the channel then falls back to
// whole-span power (New warns once; see warnSpanPowerSquelch).
func buildChannelPowerMeter(ch Channel, sampleHz float64) *channelPowerMeter {
	if ch.Mode == ModeAM || sampleHz <= 0 {
		return nil
	}
	return newChannelPowerMeter(sampleHz)
}

// warnSpanPowerSquelch tells the operator, once, that without a sample rate
// the FM channels' squelch_dbfs measures the whole SDR span.
func warnSpanPowerSquelch(channels []Channel, sampleHz float64, log *slog.Logger) {
	if sampleHz > 0 || log == nil {
		return
	}
	for _, ch := range channels {
		if ch.Mode != ModeAM {
			log.Warn("conv: scanner sample rate is zero; squelch_dbfs measures the whole SDR span instead of each channel")
			return
		}
	}
}

// powerMeterFor returns channel idx's in-channel power meter, or nil. Same
// ownership rule as detectorFor.
func (s *Scanner) powerMeterFor(idx int) *channelPowerMeter {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if idx < 0 || idx >= len(s.powerMeters) {
		return nil
	}
	return s.powerMeters[idx]
}
