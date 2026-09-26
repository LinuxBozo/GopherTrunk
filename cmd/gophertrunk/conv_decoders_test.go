package main

import (
	"encoding/binary"
	"os"
	"testing"
	"time"

	"github.com/MattCheramie/GopherTrunk/internal/config"
	"github.com/MattCheramie/GopherTrunk/internal/events"
	"github.com/MattCheramie/GopherTrunk/internal/scanner/conventional"
	"github.com/MattCheramie/GopherTrunk/internal/storage"
)

// The factory's FleetSync decoder, fed a real-air burst at the scanner's
// channel rate, publishes onto the bus stamped with the scanner's SDR serial
// and the channel's frequency — the attribution a hopping scanner needs
// (issue #1220).
func TestConvDataDecoderFactoryPublishesWithChannelIdentity(t *testing.T) {
	bus := events.NewBus(64)
	defer bus.Close()
	sub := bus.Subscribe()
	defer sub.Close()

	factory := convDataDecoderFactory(bus, "CONV-SDR", nil)
	ch := conventional.Channel{Label: "Kenwood", FrequencyHz: 462_562_500}
	dec, err := factory(ch, conventional.DecoderFleetSync, 48_000)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile("../../internal/radio/fleetsync/afsk/testdata/fleetsync2_fleet107_unit1772_48k.cs16")
	if err != nil {
		t.Fatal(err)
	}
	iq := make([]complex64, len(b)/4)
	for i := range iq {
		iq[i] = complex(
			float32(int16(binary.LittleEndian.Uint16(b[4*i:])))/32768,
			float32(int16(binary.LittleEndian.Uint16(b[4*i+2:])))/32768)
	}
	for i := 0; i < len(iq); i += 4096 {
		dec.ProcessIQ(iq[i:min(i+4096, len(iq))])
	}

	deadline := time.After(2 * time.Second)
	for {
		select {
		case ev := <-sub.C:
			msg, ok := ev.Payload.(storage.FleetSyncMessage)
			if ev.Kind != events.KindFleetSyncMessage || !ok || !msg.CRCOK {
				continue
			}
			if msg.Fleet != 107 || msg.Unit != 1772 {
				t.Fatalf("decoded %+v, want Fleet 107 / Unit 1772", msg)
			}
			if msg.Serial != "CONV-SDR" || msg.FrequencyHz != 462_562_500 {
				t.Fatalf("serial=%q freq=%d, want CONV-SDR / 462562500", msg.Serial, msg.FrequencyHz)
			}
			return
		case <-deadline:
			t.Fatal("no CRC-valid FleetSync burst published")
		}
	}
}

func TestConvDataDecoderFactoryBuildsBothAndRejectsBadInput(t *testing.T) {
	bus := events.NewBus(8)
	defer bus.Close()
	factory := convDataDecoderFactory(bus, "S", nil)
	ch := conventional.Channel{FrequencyHz: 146_670_000}
	for _, kind := range []string{conventional.DecoderMDC1200, conventional.DecoderFleetSync} {
		if d, err := factory(ch, kind, 48_000); err != nil || d == nil {
			t.Errorf("%s: %v", kind, err)
		}
	}
	if _, err := factory(ch, "pocsag", 48_000); err == nil {
		t.Error("unknown decoder accepted")
	}
	if _, err := factory(ch, conventional.DecoderMDC1200, 48_761.9); err == nil {
		t.Error("non-integer rate accepted")
	}
	if convDataDecoderFactory(nil, "S", nil) != nil {
		t.Error("factory built without a bus")
	}
}

func TestConvChannelDecoders(t *testing.T) {
	mdc, fs := convChannelDecoders([]config.ConvChannelConfig{
		{FrequencyHz: 1},
		{FrequencyHz: 2, Decoders: []string{"fleetsync"}},
	})
	if mdc || !fs {
		t.Fatalf("mdc=%v fs=%v, want false/true", mdc, fs)
	}
}
