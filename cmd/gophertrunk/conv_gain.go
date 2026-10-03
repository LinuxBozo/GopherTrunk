package main

import (
	"log/slog"
	"strings"

	"github.com/MattCheramie/GopherTrunk/internal/config"
	"github.com/MattCheramie/GopherTrunk/internal/sdr"
)

// convChannelGain parses a scanner.conventional[].gain value (#1239) with
// the same rules as sdr.devices[].gain. Empty means "no per-channel gain";
// an unparseable value (config validation rejects it, so only a hand-built
// config reaches here) is logged and ignored.
func convChannelGain(ch config.ConvChannelConfig, log *slog.Logger) (tenthDB int, set bool) {
	if strings.TrimSpace(ch.Gain) == "" {
		return 0, false
	}
	g, ok := parseGain(ch.Gain)
	if !ok {
		log.Warn("conv: ignoring unparseable channel gain", "label", ch.Label, "freq_hz", ch.FrequencyHz, "gain", ch.Gain)
		return 0, false
	}
	if gainLooksLikeDBMistake(ch.Gain, g) {
		log.Warn("conv: channel gain looks like whole dB, but gain is tenths of a dB — \"28\" is 2.8 dB; write \"280\" or \"28.0\" for 28 dB",
			"label", ch.Label, "freq_hz", ch.FrequencyHz, "gain", ch.Gain)
	}
	return g, true
}

// convDeviceGain is the gain the pool applied to the scanner SDR at open —
// where a channel without its own gain returns to. Automatic (-1) when the
// device's gain was not configured, as the pool snapshot reports it.
func convDeviceGain(e *sdr.PoolEntry) int {
	st := e.Snapshot(true)
	if st.GainAuto {
		return -1
	}
	return st.GainTenthDB
}
