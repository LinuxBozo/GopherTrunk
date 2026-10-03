package main

import (
	"io"
	"log/slog"
	"testing"

	"github.com/MattCheramie/GopherTrunk/internal/config"
)

func TestConvChannelGainParsesLikeDeviceGain(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	for _, tc := range []struct {
		in      string
		want    int
		wantSet bool
	}{
		{"", 0, false},
		{"auto", -1, true},
		{"280", 280, true},
		{"28.0", 280, true},
		{"28,0", 280, true},
		{"loud", 0, false},
	} {
		got, set := convChannelGain(config.ConvChannelConfig{Gain: tc.in}, log)
		if got != tc.want || set != tc.wantSet {
			t.Errorf("gain %q = (%d, %v), want (%d, %v)", tc.in, got, set, tc.want, tc.wantSet)
		}
	}
}
