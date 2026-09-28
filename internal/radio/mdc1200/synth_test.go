package mdc1200

import (
	"encoding/hex"
	"strings"
	"testing"
)

// Reference vectors produced by the reference MDC1200 modem (Matthew
// Kaufman's mdc-encode-decode, mdc_encode.c): the 26 bytes it queues for a
// single packet (7 leader bytes, 5 sync bytes, 14 interleaved block bytes,
// packed most-significant bit first in transmission order) and the tone
// sequence its sampler then emits, one character per bit, '1' = 1800 Hz
// (bit changed) and '0' = 1200 Hz (bit unchanged). The reference decoder
// decodes its own audio for each of these back to the packet shown.
//
// These are the literal pins the round-trip tests cannot be (#764/#771):
// they hold the interleave, the CRC, the parity bytes, the bit order AND
// the XOR precoding against an independent implementation.
var referenceVectors = []struct {
	name    string
	op, arg uint8
	unitID  uint16
	data    string // 26 bytes, hex
	tones   string // 208 tone bits
}{
	{
		name: "ptt-id 0x1234", op: 0x01, arg: 0x80, unitID: 0x1234,
		data:  "5555555555555507092a446f8e942a2e06992204001c18a22c88",
		tones: "0111111111111111111111111111111111111111111111111111111110000100100011011011111101100110010110000100100111011110001111110011100100000101110101011011001100000110000000000001001000010100111100110011101011001100",
	},
	{
		name: "radio-check 0xABCD", op: 0x63, arg: 0x00, unitID: 0xABCD,
		data:  "5555555555555507092a446f969380c93eb63ce6002cb4569086",
		tones: "0111111111111111111111111111111111111111111111111111111110000100100011011011111101100110010110000101110111011010010000001010110110100001111011010010001010010101000000000011101011101110011111011101100011000101",
	},
	{
		// The first block of a double packet goes through the same path.
		name: "selective-call 0x0777 (block 1)", op: 0x35, arg: 0x00, unitID: 0x0777,
		data: "5555555555555507092a446f8e920847ae62342bb0539e260432",
	},
}

// packMSB packs one-byte-per-bit into bytes, most-significant bit first —
// the reference encoder's queue layout.
func packMSB(bits []byte) []byte {
	out := make([]byte, (len(bits)+7)/8)
	for i, b := range bits {
		if b&1 != 0 {
			out[i/8] |= 0x80 >> uint(i%8)
		}
	}
	return out
}

func TestBurstBitsMatchReferenceEncoderBytes(t *testing.T) {
	for _, v := range referenceVectors {
		t.Run(v.name, func(t *testing.T) {
			want, err := hex.DecodeString(v.data)
			if err != nil {
				t.Fatal(err)
			}
			bits := BurstBits(v.op, v.arg, v.unitID)
			if len(bits) != 8*len(want) {
				t.Fatalf("BurstBits length = %d bits, want %d", len(bits), 8*len(want))
			}
			got := packMSB(bits)
			if hex.EncodeToString(got) != v.data {
				t.Errorf("BurstBits =\n %x\nwant\n %x", got, want)
			}
		})
	}
}

func TestPrecodeMatchesReferenceToneSequence(t *testing.T) {
	for _, v := range referenceVectors {
		if v.tones == "" {
			continue
		}
		t.Run(v.name, func(t *testing.T) {
			tones := Precode(BurstBits(v.op, v.arg, v.unitID))
			var sb strings.Builder
			for _, tb := range tones {
				// Precode's 1 = mark = 1200 Hz = unchanged; the reference
				// prints '1' for 1800 Hz = changed.
				if tb == 1 {
					sb.WriteByte('0')
				} else {
					sb.WriteByte('1')
				}
			}
			if got := sb.String(); got != v.tones {
				t.Errorf("tone sequence differs from the reference encoder:\n got  %s\n want %s", got, v.tones)
			}
		})
	}
}

// TestPrecodeLeaderIsContinuousSpaceTone: the alternating 0x55 leader
// changes on every bit, so under XOR precoding it is one long 1800 Hz
// tone (after the first bit, which equals the precoder's 0 start state).
// A receiver that read the tones as NRZ data would see 0111… here, not the
// 0101… bit-sync pattern the leader actually carries.
func TestPrecodeLeaderIsContinuousSpaceTone(t *testing.T) {
	tones := Precode(LeaderBits(LeaderBytes))
	if tones[0] != 1 {
		t.Errorf("first tone = %d, want 1 (leader's first 0 equals the precoder's 0 start state)", tones[0])
	}
	for i, tb := range tones[1:] {
		if tb != 0 {
			t.Fatalf("leader tone %d = %d, want 0 (1800 Hz: every leader bit changes)", i+1, tb)
		}
	}
}

// TestCRC16ReferenceVectors pins the CRC against values computed from its
// definition (reflected CRC-16/CCITT, init 0, final XOR 0xFFFF) with an
// independent implementation.
func TestCRC16ReferenceVectors(t *testing.T) {
	for _, tc := range []struct {
		in   []byte
		want uint16
	}{
		{[]byte{0x01, 0x80, 0x12, 0x34}, 0x3E2E},
		{[]byte{0x63, 0x00, 0xAB, 0xCD}, 0x1568},
	} {
		if got := crc16(tc.in); got != tc.want {
			t.Errorf("crc16(%x) = %04X, want %04X", tc.in, got, tc.want)
		}
	}
}

// TestEncodeBlockDecodesBack: the synthesiser and the decoder agree on the
// interleave (the reference-literal tests above hold both to the air
// format; this holds them to each other).
func TestEncodeBlockDecodesBack(t *testing.T) {
	for _, v := range referenceVectors {
		m, ok := DecodeFrame(EncodeBlock(v.op, v.arg, v.unitID))
		if !ok || !m.CRCOK {
			t.Errorf("%s: DecodeFrame ok=%v crc=%v, want valid", v.name, ok, m.CRCOK)
		}
		if m.Op != v.op || m.Arg != v.arg || m.UnitID != v.unitID {
			t.Errorf("%s: decoded op=%02X arg=%02X unit=%04X", v.name, m.Op, m.Arg, m.UnitID)
		}
	}
}

func TestDoubleBurstBitsCarriesSecondBlock(t *testing.T) {
	bits := DoubleBurstBits(0x35, 0x00, 0x0777, [4]byte{0xDE, 0xAD, 0xBE, 0xEF})
	wantLen := 8*LeaderBytes + SyncBits + 2*FrameBits
	if len(bits) != wantLen {
		t.Fatalf("len = %d, want %d", len(bits), wantLen)
	}
	second := bits[len(bits)-FrameBits:]
	m, ok := DecodeFrame(second)
	if !ok || m.Op != 0xDE || m.Arg != 0xAD || m.UnitID != 0xBEEF {
		t.Errorf("second block decoded ok=%v op=%02X arg=%02X unit=%04X", ok, m.Op, m.Arg, m.UnitID)
	}
}
