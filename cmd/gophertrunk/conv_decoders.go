package main

import (
	"fmt"
	"log/slog"

	"github.com/MattCheramie/GopherTrunk/internal/config"
	"github.com/MattCheramie/GopherTrunk/internal/events"
	fleetsyncafsk "github.com/MattCheramie/GopherTrunk/internal/radio/fleetsync/afsk"
	mdc1200afsk "github.com/MattCheramie/GopherTrunk/internal/radio/mdc1200/afsk"
	"github.com/MattCheramie/GopherTrunk/internal/scanner/conventional"
)

// convDataDecoderFactory builds the MDC1200 / FleetSync decoders a
// scanner.conventional channel names in `decoders` (issue #1220). They are
// the same front ends the mdc1200.channels / fleetsync.channels receivers
// run, fed the scanner's channel-filtered IQ at rateHz instead of a whole
// dedicated SDR, and they publish onto the same bus kinds — so the logs,
// REST endpoints and web panels need nothing new. Each burst is stamped
// with the scanner's SDR serial and the channel's frequency, which is how a
// burst from a hopping scanner stays attributable.
func convDataDecoderFactory(bus *events.Bus, serial string, log *slog.Logger) conventional.DataDecoderFactory {
	if bus == nil {
		return nil
	}
	return func(ch conventional.Channel, kind string, rateHz float64) (conventional.DataDecoder, error) {
		rate := uint32(rateHz)
		if float64(rate) != rateHz {
			return nil, fmt.Errorf("channel rate %v Hz is not an integer", rateHz)
		}
		source := fmt.Sprintf("%s@%d", serial, ch.FrequencyHz)
		switch kind {
		case conventional.DecoderMDC1200:
			return mdc1200afsk.New(mdc1200afsk.Options{
				InputRateHz: rate,
				SourceName:  source,
				Serial:      serial,
				FrequencyHz: ch.FrequencyHz,
				Bus:         bus,
				Log:         log,
			})
		case conventional.DecoderFleetSync:
			return fleetsyncafsk.New(fleetsyncafsk.Options{
				InputRateHz: rate,
				SourceName:  source,
				Serial:      serial,
				FrequencyHz: ch.FrequencyHz,
				Bus:         bus,
				Log:         log,
			})
		}
		return nil, fmt.Errorf("unknown decoder %q", kind)
	}
}

// convChannelDecoders reports whether any scanner.conventional channel runs
// the MDC1200 / FleetSync decoder.
func convChannelDecoders(chs []config.ConvChannelConfig) (mdc, fs bool) {
	for _, ch := range chs {
		for _, d := range ch.Decoders {
			switch d {
			case conventional.DecoderMDC1200:
				mdc = true
			case conventional.DecoderFleetSync:
				fs = true
			}
		}
	}
	return mdc, fs
}
