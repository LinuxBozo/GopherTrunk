package afsk

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"github.com/MattCheramie/GopherTrunk/internal/events"
)

// TestMDC1200RealAirSlice decodes a real Motorola transmission through the
// production front end: a 0.7 s channelized (48 kHz, cs16) slice of the
// capture @v2maldo posted for issue #1220 — the end-of-transmission PTT ID
// from unit 0x1777 on 447.100 MHz (see testdata/README.md). It is the
// on-air pin the synthetic round-trips and even the reference-encoder audio
// cannot be (#764/#771): a real radio's leader length, deviation, clipping
// and keyup transient. The clip is ADC-clipped (the handheld was in the
// same room) and decodes anyway, like the #836 DMR captures.
func TestMDC1200RealAirSlice(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "mdc1200_unit1777_pttid_end_48k.cs16"))
	if err != nil {
		t.Fatal(err)
	}
	iq := make([]complex64, len(raw)/4)
	for i := range iq {
		re := int16(binary.LittleEndian.Uint16(raw[i*4:]))
		im := int16(binary.LittleEndian.Uint16(raw[i*4+2:]))
		iq[i] = complex(float32(re)/32768, float32(im)/32768)
	}
	for _, chunk := range []int{4096, 500} {
		bus := events.NewBus(64)
		sub := bus.Subscribe()
		r, err := New(Options{InputRateHz: 48000, Bus: bus})
		if err != nil {
			t.Fatal(err)
		}
		for i := 0; i < len(iq); i += chunk {
			r.ProcessIQ(iq[i:min(i+chunk, len(iq))])
		}
		msgs := drainMDC(sub)
		sub.Close()
		bus.Close()
		for _, m := range msgs {
			t.Logf("chunk %d: burst crc_ok=%v unit=%04X op=%02X arg=%02X %s raw=%s", chunk, m.CRCOK, m.UnitID, m.Op, m.Arg, m.Operation, m.RawHex)
		}
		if got := crcValid(msgs, 0x01, 0x00, 0x1777); got != 1 {
			t.Fatalf("chunk %d: %d CRC-valid PTT ID (end) bursts from unit 1777, want 1 (%d framed; front=%+v inner=%+v)",
				chunk, got, len(msgs), r.Stats(), r.Inner().Stats())
		}
	}
}
