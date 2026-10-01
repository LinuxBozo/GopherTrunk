package acars

import (
	"bytes"
	"encoding/hex"
	"math/rand"
	"testing"
)

// realAirFrames are blocks received off the air: the raw bytes (mode
// through BCS, parity bits as transmitted) of messages in the real-air
// recording acarsdec ships as test.wav, as this package's receiver framed
// them. Each one validates its own block check, and the fields below are the
// ones acarsdec itself prints for the same recording (an independent
// decoder), so they pin bit order, parity, the CRC convention, the BCS byte
// order and the field layout against real air — not against this package's
// own encoder (#764/#771).
var realAirFrames = []struct {
	name                string
	raw                 string
	mode                byte
	addr, label, ack    string
	blk                 byte
	downlink            bool
	msgNo, flight, text string
}{
	{
		// Mode x, no text: the ETX sits in the STX slot. Label "_" DEL ("_d").
		name: "LN-DYY _d squitter-style ack", raw: "f8ae4cceadc4d9d9b5df7fc183337c",
		mode: 'x', addr: "LN-DYY", label: "_d", ack: "5", blk: 'A',
	},
	{
		name: "PH-BXR 5V downlink", raw: "45aed0c8adc2585215b5d63402d3b5b3c1cb4c31b638318314fc",
		mode: 'E', addr: "PH-BXR", label: "5V", ack: "!", blk: '4', downlink: true,
		msgNo: "S53A", flight: "KL1681",
	},
	{
		name: "G-DBCK _d downlink", raw: "32aec7adc4c243cb57df7fb002d3b634c1c2c1b0b3315483ca9f",
		mode: '2', addr: "G-DBCK", label: "_d", ack: "W", blk: '0', downlink: true,
		msgNo: "S64A", flight: "BA031T",
	},
}

func splitRaw(t *testing.T, s string) ([]byte, [2]byte) {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b[:len(b)-2], [2]byte{b[len(b)-2], b[len(b)-1]}
}

func TestCRCIsKermit(t *testing.T) {
	// CRC-16/KERMIT's catalogue check value — an independent pin of the
	// polynomial, reflection and (zero) initial value.
	if got := CRC([]byte("123456789")); got != 0x2189 {
		t.Fatalf("CRC(123456789) = %#04x, want 0x2189 (CRC-16/KERMIT)", got)
	}
}

func TestDecodeRealAirFrames(t *testing.T) {
	for _, f := range realAirFrames {
		t.Run(f.name, func(t *testing.T) {
			body, bcs := splitRaw(t, f.raw)
			if CRC(append(append([]byte(nil), body...), bcs[0], bcs[1])) != 0 {
				t.Fatal("literal frame does not validate its own block check")
			}
			m := DecodeBlock(body, bcs)
			if !m.CRCOK || m.Corrected != 0 {
				t.Fatalf("CRCOK=%v corrected=%d, want a clean block", m.CRCOK, m.Corrected)
			}
			if m.Mode != f.mode || m.Address != f.addr || m.Label != f.label || m.AckString() != f.ack ||
				m.BlockID != f.blk || m.Downlink != f.downlink || m.MsgNo != f.msgNo ||
				m.FlightID != f.flight || m.Text != f.text || m.More {
				t.Fatalf("decoded %+v", m)
			}
		})
	}
}

// TestEncoderReproducesRealAirFrames pins the synthesiser to real air: it
// must re-create the received blocks byte for byte from their fields, so the
// synthetic streams the receiver tests use are faithful transmitters.
func TestEncoderReproducesRealAirFrames(t *testing.T) {
	for _, f := range realAirFrames {
		text := f.msgNo + f.flight + f.text
		ack := byte(f.ack[0])
		if f.ack == "!" {
			ack = charNAK
		}
		label := f.label
		if label[1] == 'd' {
			label = string([]byte{label[0], charDEL})
		}
		b := Block{Mode: f.mode, Address: f.addr, Ack: ack, Label: label, BlockID: f.blk, Text: text}
		enc := EncodeBlock(b)
		want, _ := hex.DecodeString(f.raw)
		// pre-key + sync + SOH, then the block, then DEL.
		head := []byte{charPlus, charStar, charSYN, charSYN, charSOH}
		if !bytes.Equal(enc[PreKeyChars:PreKeyChars+5], head) {
			t.Fatalf("%s: sync %x", f.name, enc[PreKeyChars:PreKeyChars+5])
		}
		got := enc[PreKeyChars+5 : len(enc)-1]
		if !bytes.Equal(got, want) {
			t.Fatalf("%s:\n got %x\nwant %x", f.name, got, want)
		}
		if enc[len(enc)-1] != charDEL {
			t.Fatalf("%s: trailing %#x, want DEL", f.name, enc[len(enc)-1])
		}
	}
}

func longBlock() Block {
	return Block{Mode: '2', Address: "N123GT", Ack: charNAK, Label: "H1", BlockID: '5',
		Text: "M01AGT0042#DFB/ALT 37000 SPD 0.82 FUEL 123.4 TEMP -56 WIND 270/085\r\nEND"}
}

func TestCorrectsSingleBitErrors(t *testing.T) {
	enc := EncodeBlock(longBlock())
	block := enc[PreKeyChars+5 : len(enc)-1]
	body, bcs := block[:len(block)-2], [2]byte{block[len(block)-2], block[len(block)-1]}
	clean := DecodeBlock(body, bcs)
	if !clean.CRCOK {
		t.Fatal("clean block failed")
	}
	// Every single-bit error anywhere in the block, BCS included.
	for pos := 0; pos < 8*len(block); pos++ {
		b := append([]byte(nil), block...)
		b[pos/8] ^= 1 << (pos % 8)
		m := DecodeBlock(b[:len(b)-2], [2]byte{b[len(b)-2], b[len(b)-1]})
		if !m.CRCOK || m.Corrected != 1 || m.Text != clean.Text || m.Address != clean.Address {
			t.Fatalf("bit %d: CRCOK=%v corrected=%d text=%q", pos, m.CRCOK, m.Corrected, m.Text)
		}
	}
}

func TestCorrectsThreeParityErrorsAndAdjacentPairs(t *testing.T) {
	enc := EncodeBlock(longBlock())
	block := enc[PreKeyChars+5 : len(enc)-1]
	n := len(block) - 2
	want := DecodeBlock(block[:n], [2]byte{block[n], block[n+1]})
	rng := rand.New(rand.NewSource(3))
	okThree, okPair := 0, 0
	for trial := 0; trial < 300; trial++ {
		b := append([]byte(nil), block...)
		perm := rng.Perm(n)[:3]
		for _, k := range perm {
			b[k] ^= 1 << rng.Intn(8)
		}
		m := DecodeBlock(b[:n], [2]byte{b[n], b[n+1]})
		if m.CRCOK {
			if m.Text != want.Text || m.Corrected != 3 {
				t.Fatalf("three-error trial %d repaired to the WRONG block: %q", trial, m.Text)
			}
			okThree++
		}
		b = append([]byte(nil), block...)
		k, i := rng.Intn(n), rng.Intn(7)
		b[k] ^= 3 << i
		m = DecodeBlock(b[:n], [2]byte{b[n], b[n+1]})
		if m.CRCOK {
			if m.Text != want.Text || m.Corrected != 2 {
				t.Fatalf("pair trial %d repaired to the WRONG block: %q", trial, m.Text)
			}
			okPair++
		}
	}
	// The uniqueness guard refuses the (rare) ambiguous cases; the bulk
	// must still repair.
	if okThree < 270 || okPair < 270 {
		t.Fatalf("repaired %d/300 three-error and %d/300 adjacent-pair blocks", okThree, okPair)
	}
}

// TestRepairNeverValidatesGarbage: the corrector is fed the worst case for
// a 16-bit check — random 7-bit characters with VALID parity except for 0..3
// broken ones (so the parity-located search runs every time) and a random
// BCS. Without the guards ~0.4% of these "repair" into a CRC-valid block
// (n candidates / 65536); with them none may.
func TestRepairNeverValidatesGarbage(t *testing.T) {
	rng := rand.New(rand.NewSource(11))
	accepted := 0
	const trials = 20000
	for i := 0; i < trials; i++ {
		n := headerLen + rng.Intn(60)
		b := make([]byte, n+2)
		rng.Read(b)
		for k := 0; k < n; k++ {
			b[k] = withParity(b[k])
		}
		b[headerLen-1] = charSTX
		b[n-1] = charETX
		for e := rng.Intn(maxParityErrors + 1); e > 0; e-- {
			b[rng.Intn(n)] ^= 1 << rng.Intn(8)
		}
		if DecodeBlock(b[:n], [2]byte{b[n], b[n+1]}).CRCOK {
			accepted++
		}
	}
	t.Logf("accepted %d", accepted)
	if accepted > 0 {
		t.Fatalf("%d/%d garbage blocks validated", accepted, trials)
	}
}

func framerFeed(f *Framer, bits []byte) {
	for _, b := range bits {
		f.Push(b)
	}
}

func TestFramerFindsBlocksInBothPolarities(t *testing.T) {
	rng := rand.New(rand.NewSource(5))
	noise := func(n int) []byte {
		out := make([]byte, n)
		for i := range out {
			out[i] = byte(rng.Intn(2))
		}
		return out
	}
	b1 := longBlock()
	b2 := Block{Mode: '2', Address: "G-DBCK", Ack: 'W', Label: "_\x7f", BlockID: '0', Text: "S64ABA031T"}
	var got []Message
	f := NewFramer(func(m Message) { got = append(got, m) })
	stream := append(noise(500), Bits(EncodeBlock(b1))...)
	stream = append(stream, noise(300)...)
	inv := Bits(EncodeBlock(b2))
	for i := range inv {
		inv[i] ^= 1
	}
	stream = append(stream, inv...)
	stream = append(stream, noise(500)...)
	framerFeed(f, stream)
	if len(got) != 2 || !got[0].CRCOK || !got[1].CRCOK {
		t.Fatalf("got %d blocks: %+v", len(got), got)
	}
	if got[0].FlightID != "GT0042" || got[1].Address != "G-DBCK" || got[1].Label != "_d" {
		t.Fatalf("fields: %+v / %+v", got[0], got[1])
	}
	if s := f.Stats(); s.SyncLocks != 2 || s.CRCOK != 2 {
		t.Fatalf("stats %+v", s)
	}
}

// TestFramerRecoversADamagedSuffix: a bit error in ETX means the framer
// never sees the suffix; the block-ending DEL arrives instead and the
// characters before it are suffix + BCS, which the corrector repairs.
func TestFramerRecoversADamagedSuffix(t *testing.T) {
	enc := EncodeBlock(longBlock())
	suffix := len(enc) - 4 // ETX, BCS lo, BCS hi, DEL
	if enc[suffix] != charETX {
		t.Fatalf("layout: %#x", enc[suffix])
	}
	enc[suffix] ^= 0x01 // 0x83 → 0x82: no longer ETX, parity now even
	var got []Message
	f := NewFramer(func(m Message) { got = append(got, m) })
	framerFeed(f, Bits(enc))
	if len(got) != 1 || !got[0].CRCOK || got[0].FlightID != "GT0042" {
		t.Fatalf("got %+v", got)
	}
}

func TestFramerBusyAndReset(t *testing.T) {
	var got []Message
	f := NewFramer(func(m Message) { got = append(got, m) })
	bits := Bits(EncodeBlock(longBlock()))
	half := (PreKeyChars + 5 + 20) * 8
	framerFeed(f, bits[:half])
	if !f.Busy() {
		t.Fatal("not busy mid-block")
	}
	f.Reset()
	if f.Busy() {
		t.Fatal("busy after Reset")
	}
	framerFeed(f, bits[half:])
	if len(got) != 0 {
		t.Fatalf("a reset block still emitted: %+v", got)
	}
}

func TestFramerAbortsRunawayLock(t *testing.T) {
	f := NewFramer(func(Message) {})
	bits := Bits(EncodeBlock(longBlock()))
	lock := (PreKeyChars + 5) * 8
	framerFeed(f, bits[:lock])
	if !f.Busy() {
		t.Fatal("no lock")
	}
	// Valid-parity characters with no suffix: the framer must give up at
	// the longest legal block instead of holding the channel for ever.
	framerFeed(f, Bits(bytes.Repeat([]byte{withParity('A')}, maxBodyLen)))
	if f.Busy() || f.Stats().Aborted != 1 {
		t.Fatalf("busy=%v stats=%+v", f.Busy(), f.Stats())
	}
}
