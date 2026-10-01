package main

import (
	"encoding/binary"
	"os"
	"testing"
	"time"

	"github.com/MattCheramie/GopherTrunk/internal/config"
	"github.com/MattCheramie/GopherTrunk/internal/events"
	"github.com/MattCheramie/GopherTrunk/internal/radio/acars"
	acarsrx "github.com/MattCheramie/GopherTrunk/internal/radio/acars/receiver"
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

// The factory's ACARS decoder (#1231), fed an AM channel at the scanner's
// channel rate, publishes onto the bus with the channel's identity.
func TestConvDataDecoderFactoryPublishesACARS(t *testing.T) {
	bus := events.NewBus(64)
	defer bus.Close()
	sub := bus.Subscribe()
	defer sub.Close()
	dec, err := convDataDecoderFactory(bus, "CONV-SDR", nil)(
		conventional.Channel{Label: "ACARS", FrequencyHz: 131_550_000, Mode: "am"}, conventional.DecoderACARS, 48_000)
	if err != nil {
		t.Fatal(err)
	}
	blk := acars.Block{Mode: '2', Address: "N123GT", Ack: 0x15, Label: "H1", BlockID: '5', Text: "M01AGT0042"}
	iq := append(make([]complex64, 4800), acarsrx.SynthAMIQ(acars.EncodeBlock(blk), 48_000, 700, 0.6)...)
	iq = append(iq, make([]complex64, 4800)...)
	for i := 0; i < len(iq); i += 4096 {
		dec.ProcessIQ(iq[i:min(i+4096, len(iq))])
	}
	deadline := time.After(2 * time.Second)
	for {
		select {
		case ev := <-sub.C:
			msg, ok := ev.Payload.(storage.ACARSMessage)
			if ev.Kind != events.KindACARSMessage || !ok || !msg.CRCOK {
				continue
			}
			if msg.Address != "N123GT" || msg.FlightID != "GT0042" || msg.Serial != "CONV-SDR" || msg.FrequencyHz != 131_550_000 {
				t.Fatalf("published %+v", msg)
			}
			return
		case <-deadline:
			t.Fatal("no CRC-valid ACARS block published")
		}
	}
}

func TestConvDataDecoderFactoryBuildsBothAndRejectsBadInput(t *testing.T) {
	bus := events.NewBus(8)
	defer bus.Close()
	factory := convDataDecoderFactory(bus, "S", nil)
	ch := conventional.Channel{FrequencyHz: 146_670_000}
	for _, kind := range []string{conventional.DecoderMDC1200, conventional.DecoderFleetSync, conventional.DecoderACARS} {
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
	mdc, fs, acars := convChannelDecoders([]config.ConvChannelConfig{
		{FrequencyHz: 1},
		{FrequencyHz: 2, Decoders: []string{"fleetsync"}},
		{FrequencyHz: 3, Mode: "am", Decoders: []string{"acars"}},
	})
	if mdc || !fs || !acars {
		t.Fatalf("mdc=%v fs=%v acars=%v, want false/true/true", mdc, fs, acars)
	}
}
