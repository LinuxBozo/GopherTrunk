package conventional

// Per-channel gain (issue #1239).
//
// One scanner SDR serves a scan list that can mix bands and modes — VHF FM
// next to air-band AM next to UHF — and no single tuner gain suits all of
// them; "auto" in particular serves AM badly. A channel may therefore carry
// its own gain, applied to the scanner's SDR before the channel is tuned.
// Channels without one run at the device's configured gain
// (Options.DefaultGainTenthDB), so a mixed list returns to that gain after
// leaving a channel that set its own.
//
// The scanner only touches the gain when at least one channel sets one: a
// scan list without per-channel gains behaves exactly as before. Once any
// channel does, the scanner owns the device's gain — a gain changed from
// elsewhere (the API) is overwritten at the next channel whose gain differs
// from the one the scanner last applied.

// GainSetter is the subset of sdr.Device the scanner needs for per-channel
// gain. tenthDB follows sdr.Device.SetGain: tenths of a dB, negative selects
// the tuner's automatic gain.
type GainSetter interface {
	SetGain(tenthDB int) error
}

// usesChannelGain reports whether any channel sets its own gain.
func usesChannelGain(channels []Channel) bool {
	for _, ch := range channels {
		if ch.GainSet {
			return true
		}
	}
	return false
}

// applyChannelGain programs ch's gain (or the device default for a channel
// without one) on the scanner's SDR, skipping the write when the gain is
// already set. A failed write is logged once per value and the scan carries
// on at whatever gain the device holds.
func (s *Scanner) applyChannelGain(ch Channel) {
	if s.opts.Gain == nil {
		return
	}
	s.mu.Lock()
	active := s.gainActive || ch.GainSet
	s.gainActive = active
	want := s.opts.DefaultGainTenthDB
	if ch.GainSet {
		want = ch.GainTenthDB
	}
	skip := !active || want == s.appliedGain
	s.mu.Unlock()
	if skip {
		return
	}
	if err := s.opts.Gain.SetGain(want); err != nil {
		s.mu.Lock()
		warned := s.gainWarned[want]
		s.gainWarned[want] = true
		s.mu.Unlock()
		if !warned {
			s.log.Warn("conv: per-channel gain write failed; scanning at the device's current gain",
				"freq_hz", ch.FrequencyHz, "label", ch.Label, "gain_tenth_db", want, "err", err)
		}
		return
	}
	s.mu.Lock()
	s.appliedGain = want
	s.mu.Unlock()
}
