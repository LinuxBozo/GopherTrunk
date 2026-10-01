package config

import "testing"

// scanner.conventional[].decoders accepts mdc1200 / fleetsync and rejects
// anything else or a repeat (issue #1220).
func TestValidateConvChannelDecoders(t *testing.T) {
	base := ConvChannelConfig{FrequencyHz: 462_562_500}
	for _, ok := range [][]string{nil, {"mdc1200"}, {"fleetsync"}, {"mdc1200", "fleetsync"}} {
		ch := base
		ch.Decoders = ok
		if err := validateConvChannel(0, ch); err != nil {
			t.Errorf("decoders %v rejected: %v", ok, err)
		}
	}
	for _, bad := range [][]string{{"pocsag"}, {"MDC1200"}, {"fleetsync", "fleetsync"}} {
		ch := base
		ch.Decoders = bad
		if err := validateConvChannel(0, ch); err == nil {
			t.Errorf("decoders %v accepted", bad)
		}
	}
}

// acars (#1231) is accepted only on an AM channel: ACARS is MSK on an AM
// carrier, and an FM channel's squelch and voice chain do not fit it.
func TestValidateConvChannelACARSNeedsAM(t *testing.T) {
	am := ConvChannelConfig{FrequencyHz: 131_550_000, Mode: "am", Decoders: []string{"acars"}}
	if err := validateConvChannel(0, am); err != nil {
		t.Fatalf("acars on an AM channel rejected: %v", err)
	}
	for _, mode := range []string{"", "fm", "nfm"} {
		ch := am
		ch.Mode = mode
		if err := validateConvChannel(0, ch); err == nil {
			t.Errorf("acars accepted on mode %q", mode)
		}
	}
}
