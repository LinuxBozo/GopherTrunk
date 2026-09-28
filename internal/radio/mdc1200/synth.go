package mdc1200

// Burst synthesis — the transmitter side of the protocol, for the DSP
// front end's tests and the capture replay harness.
//
// Everything here follows the on-air format as the reference MDC1200
// modem (Matthew Kaufman's mdc-encode-decode, the decoder every MDC1200
// tool in the field is built on) describes it, and synth_test.go pins the
// output against LITERAL byte and tone sequences produced by that encoder
// — the only kind of test that can catch this package drifting from the
// air interface (#764/#771: an encoder and decoder that share one wrong
// layout pass every round-trip). The facts:
//
//   - A burst is a leader of 0x55 bytes (bit-sync), the 40-bit sync word
//     0x07092A446F, then one 14-byte data block per packet (two for a
//     double packet). Bytes go out most-significant bit first.
//   - The block is op, arg, unit ID (big-endian), CRC-16 (little-endian),
//     a zero byte, and seven convolutional-parity bytes, column-
//     interleaved over a 16×7 grid (bit L of the LSB-first-unpacked block
//     lands at wire position (L mod 7)·16 + L div 7).
//   - The line code is XOR precoding onto MSK tones: each data bit is
//     compared with the previous one, and the radio sends one cycle of
//     1200 Hz when the bit is the SAME and 1.5 cycles of 1800 Hz when it
//     CHANGED. The 1200/1800 tone pair at 1200 baud is MSK (h = 0.5), and
//     the precoding is what makes the audio's sign at each bit instant
//     equal the data bit — the trick the reference decoder's one-point
//     sampler relies on. The precoder starts from a previous bit of 0.
//
// The last point is the one the receiver got wrong for the whole life of
// the decoder (#1220): it sliced the tone sequence itself as NRZ data, so
// the sync word could never match a real radio.

// LeaderBytes is the length of the bit-sync leader the reference encoder
// sends before the sync word. Radios send longer preambles; the framer
// only needs enough for symbol timing to settle.
const LeaderBytes = 7

// BlockBytes builds the 14 data bytes of one block: the four header bytes,
// the CRC, a zero byte and the seven parity bytes — NOT yet interleaved.
func BlockBytes(op, arg uint8, unitID uint16) [14]byte {
	var data [14]byte
	data[0] = op
	data[1] = arg
	data[2] = byte(unitID >> 8)
	data[3] = byte(unitID)
	crc := crc16(data[:4])
	data[4] = byte(crc) // low byte first on the wire
	data[5] = byte(crc >> 8)
	data[6] = 0
	var head [7]byte
	copy(head[:], data[:7])
	parity := fecParity(head)
	copy(data[7:], parity[:])
	return data
}

// fecParity computes the seven redundancy bytes that follow the header in
// every block: a rate-1/2 convolutional code run over the first seven
// bytes' bits, LSB first, with a 7-stage shift register whose taps are
// stages 0, 2, 5 and 6. The register carries across byte boundaries. The
// decoder does not yet use the redundancy to correct errors; it is
// generated so synthetic bursts carry what a radio carries and so the
// literal reference vectors match byte-for-byte.
func fecParity(data [7]byte) [7]byte {
	var out [7]byte
	var csr [7]byte
	for i := 0; i < 7; i++ {
		for j := 0; j < 8; j++ {
			for k := 6; k > 0; k-- {
				csr[k] = csr[k-1]
			}
			csr[0] = (data[i] >> uint(j)) & 1
			b := csr[0] ^ csr[2] ^ csr[5] ^ csr[6]
			out[i] |= b << uint(j)
		}
	}
	return out
}

// Interleave lays the 14 block bytes out as the 112 wire bits (one byte
// per bit, in transmission order): the block is unpacked LSB-first into
// logical bits and column-interleaved so logical bit L goes to wire
// position (L mod 7)·16 + L div 7. This is the exact inverse of
// deinterleave.
func Interleave(data [14]byte) []byte {
	var lbits [FrameBits]byte
	for i := 0; i < 14; i++ {
		for j := 0; j < 8; j++ {
			lbits[i*8+j] = (data[i] >> uint(j)) & 1
		}
	}
	bits := make([]byte, FrameBits)
	for l := 0; l < FrameBits; l++ {
		bits[(l%7)*16+l/7] = lbits[l]
	}
	return bits
}

// EncodeBlock is BlockBytes followed by Interleave: the 112 wire bits of
// one packet's data block, in transmission order.
func EncodeBlock(op, arg uint8, unitID uint16) []byte {
	return Interleave(BlockBytes(op, arg, unitID))
}

// SyncWordBits returns the 40-bit sync word in transmission order (MSB first).
func SyncWordBits() []byte {
	out := make([]byte, SyncBits)
	for i := range out {
		out[i] = byte((SyncWord >> uint(SyncBits-1-i)) & 1)
	}
	return out
}

// LeaderBits returns n leader bytes of 0x55 as data bits in transmission
// order — the alternating 0101… bit-sync pattern, which the precoder turns
// into a continuous 1800 Hz tone.
func LeaderBits(n int) []byte {
	out := make([]byte, 0, 8*n)
	for i := 0; i < 8*n; i++ {
		out = append(out, byte(i&1))
	}
	return out
}

// BurstBits is the full DATA bit sequence of a single-packet burst as the
// reference encoder sends it: LeaderBytes of 0x55, the sync word, and
// the interleaved block. Feed it to Precode for the tone sequence.
func BurstBits(op, arg uint8, unitID uint16) []byte {
	bits := LeaderBits(LeaderBytes)
	bits = append(bits, SyncWordBits()...)
	return append(bits, EncodeBlock(op, arg, unitID)...)
}

// DoubleBurstBits is BurstBits for a double-packet opcode followed by the
// second block, whose four header bytes are the extra bytes (the
// reference encoder builds the second block through the same CRC and
// parity path as the first).
func DoubleBurstBits(op, arg uint8, unitID uint16, extra [4]byte) []byte {
	bits := BurstBits(op, arg, unitID)
	return append(bits, EncodeBlock(extra[0], extra[1], uint16(extra[2])<<8|uint16(extra[3]))...)
}

// Precode applies the XOR precoding: the tone bit for each data bit is 1
// (mark, 1200 Hz) when the bit equals the previous one and 0 (space,
// 1800 Hz) when it changed, starting from a previous bit of 0 as the
// reference encoder does. The result is what an FFSK modulator with
// mark = 1200 Hz / space = 1800 Hz (demod.ModulateFFSK) takes.
func Precode(data []byte) []byte {
	out := make([]byte, len(data))
	var prev byte
	for i, b := range data {
		b &= 1
		if b == prev {
			out[i] = 1
		}
		prev = b
	}
	return out
}
