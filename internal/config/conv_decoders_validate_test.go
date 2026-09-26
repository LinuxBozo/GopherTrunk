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
