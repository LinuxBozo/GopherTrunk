package config

import "testing"

// scanner.conventional[].mode accepts am (issue #1219); squelch_cn_db must
// not be negative.
func TestValidateConvChannelAM(t *testing.T) {
	ok := ConvChannelConfig{FrequencyHz: 118_700_000, Mode: "am", SquelchCNDb: 12}
	if err := validateConvChannel(0, ok); err != nil {
		t.Errorf("mode am rejected: %v", err)
	}
	for _, bad := range []ConvChannelConfig{
		{FrequencyHz: 118_700_000, Mode: "usb"},
		{FrequencyHz: 118_700_000, Mode: "am", SquelchCNDb: -1},
	} {
		if err := validateConvChannel(0, bad); err == nil {
			t.Errorf("%+v accepted", bad)
		}
	}
}
