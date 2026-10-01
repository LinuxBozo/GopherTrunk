// Package acars decodes plain-old ACARS (Aircraft Communications Addressing
// and Reporting System, ARINC 618 "VHF Category A/B" air/ground link): the
// 2400 bit/s MSK-on-AM data link on the VHF air band (131.550, 131.525,
// 130.025, 129.125 MHz and friends). Issue #1231.
//
// This package is the protocol layer only: block framing, odd parity, the
// block check, single/double-bit error correction and the message fields.
// The DSP that turns AM IQ into a bit stream lives in
// internal/radio/acars/receiver.
//
// Wire format of one block, every character 7-bit ASCII sent LSB first with
// an ODD parity bit in bit 7 (the block check characters are 16 raw bits):
//
//	pre-key   ≥16 × 0xFF (all ones)   — lets the receiver's clock settle
//	sync      '+' '*' SYN SYN         — 0xAB 0x2A 0x16 0x16 with parity
//	SOH       0x01
//	mode      1 char                  — '2' = Category A, 'A'..'Z' etc.
//	address   7 chars                 — aircraft registration, '.'-padded
//	ack       1 char                  — technical ack / NAK (0x15)
//	label     2 chars                 — message type ("H1", "Q0", "_\x7f" …)
//	block id  1 char                  — '0'..'9' downlink, 'A'..'Z' uplink
//	STX       0x02                    — or ETX straight away: no text
//	text      ≤220 chars              — downlink: msg no (4) + flight (6) + body
//	suffix    ETX 0x03 (last block) | ETB 0x17 (more blocks follow)
//	BCS       2 bytes                 — block check sequence, low byte first
//	DEL       0x7F
//
// The block check is the reflected CRC-CCITT (polynomial 0x8408 reflected,
// initial value 0, no final XOR — "CRC-16/KERMIT") computed over every
// character from mode through the suffix INCLUDING their parity bits; run
// over those characters and the two BCS bytes it yields zero.
//
// Provenance (#764/#771 discipline — no field layout from memory): the
// layout, parity sense, bit order, CRC convention and line code were read
// from acarsdec (github.com/f00b4r0/acarsdec, GPL — read for protocol
// facts only, nothing ported) and then CONFIRMED independently in both
// directions: this package decodes the seven real-air messages in
// acarsdec's own test recording to the same fields acarsdec prints, and
// acarsdec decodes audio synthesised by this package's encoder. The literal
// frames in testdata_test.go are from that recording.
package acars

import (
	"encoding/hex"
	"fmt"
	"math/bits"
	"strings"
)

// Character constants, parity bit included where the wire carries it.
const (
	charPreKey = 0xFF
	charPlus   = '+' | 0x80 // 0xAB
	charStar   = '*'        // 0x2A (already odd)
	charSYN    = 0x16
	charSOH    = 0x01
	charSTX    = 0x02
	charETX    = 0x03 | 0x80 // 0x83
	charETB    = 0x17 | 0x80 // 0x97
	charDEL    = 0x7F
	charNAK    = 0x15
)

const (
	// headerLen is mode + address + ack + label + block id + STX/ETX —
	// the shortest legal block body (no text).
	headerLen = 1 + 7 + 1 + 2 + 1 + 1
	// MaxTextLen is the longest text a block may carry.
	MaxTextLen = 220
	// maxBodyLen is header + text + suffix: the longest run of characters
	// the framer collects before giving up on a block.
	maxBodyLen = headerLen + MaxTextLen + 1
	// BaudHz is the ACARS signalling rate.
	BaudHz = 2400
)

// Message is one decoded ACARS block.
type Message struct {
	Mode     byte   // '2', 'A'.. 'Z', … (0 when the block failed to frame)
	Address  string // aircraft registration with leading '.' padding removed
	Ack      byte   // technical acknowledgement; charNAK (0x15) means NAK
	Label    string // two-character label; a DEL second char renders as 'd' ("_d")
	BlockID  byte   // '0'..'9' downlink (air→ground), letters uplink, 0 = squitter
	Downlink bool   // BlockID is a digit
	MsgNo    string // downlink only: 4-char message sequence number
	FlightID string // downlink only: 6-char flight identifier
	Text     string // message body (control characters kept, parity stripped)
	More     bool   // suffix was ETB: further blocks of this message follow

	CRCOK     bool   // the block check validated (possibly after correction)
	Corrected int    // bits repaired by parity/BCS error correction
	RawHex    string // the received block, mode through BCS, before correction
}

// AckString renders Ack the way decoders conventionally print it: '!' for
// a NAK, the character otherwise.
func (m Message) AckString() string {
	switch m.Ack {
	case charNAK:
		return "!"
	case 0:
		return ""
	}
	return string(rune(m.Ack))
}

// Summary is a one-line rendering for logs and the web panel.
func (m Message) Summary() string {
	var b strings.Builder
	fmt.Fprintf(&b, "mode=%c reg=%s label=%s blk=%s ack=%s", printable(m.Mode), m.Address, m.Label, blockString(m.BlockID), m.AckString())
	if m.FlightID != "" {
		fmt.Fprintf(&b, " flight=%s", strings.TrimSpace(m.FlightID))
	}
	if m.MsgNo != "" {
		fmt.Fprintf(&b, " msgno=%s", m.MsgNo)
	}
	if m.Text != "" {
		t := strings.NewReplacer("\r", " ", "\n", " ").Replace(m.Text)
		fmt.Fprintf(&b, " text=%q", t)
	}
	return b.String()
}

func printable(c byte) byte {
	if c < 0x20 || c >= 0x7F {
		return '.'
	}
	return c
}

func blockString(c byte) string {
	if c == 0 {
		return "."
	}
	return string(rune(printable(c)))
}

// oddParity reports whether c has odd parity (the ACARS rule).
func oddParity(c byte) bool { return bits.OnesCount8(c)&1 == 1 }

// withParity returns the 7-bit character c with bit 7 set so the byte has
// odd parity.
func withParity(c byte) byte {
	c &= 0x7F
	if !oddParity(c) {
		c |= 0x80
	}
	return c
}

// crcTable is the reflected CRC-CCITT table (polynomial 0x1021 reflected =
// 0x8408).
var crcTable = func() (t [256]uint16) {
	for i := range t {
		c := uint16(i)
		for k := 0; k < 8; k++ {
			if c&1 != 0 {
				c = c>>1 ^ 0x8408
			} else {
				c >>= 1
			}
		}
		t[i] = c
	}
	return
}()

// CRC computes the ACARS block check over p (initial value 0, no final XOR).
// Over a block body followed by its two BCS bytes the result is zero.
func CRC(p []byte) uint16 {
	var c uint16
	for _, b := range p {
		c = c>>8 ^ crcTable[byte(c)^b]
	}
	return c
}

// syndromeTable[d][i] is the block-check contribution of flipping bit i of a
// byte that has d bytes after it in the checked span. With initial value 0
// and no final XOR the CRC is linear, CRC(x⊕e) = CRC(x)⊕CRC(e), and a lone
// set bit's CRC depends only on how many bytes follow it — so one table
// covers every position. Spans are at most maxBodyLen+2 bytes.
var syndromeTable = func() (t [maxBodyLen + 2][8]uint16) {
	buf := make([]byte, maxBodyLen+2)
	for d := range t {
		for i := 0; i < 8; i++ {
			for k := range buf[:d+1] {
				buf[k] = 0
			}
			buf[0] = 1 << i
			t[d][i] = CRC(buf[:d+1])
		}
	}
	return
}()

// maxParityErrors bounds the parity-located correction search (8^n bit
// combinations). Beyond three wrong characters the block is too damaged
// for the 16-bit check to arbitrate between candidates reliably.
const maxParityErrors = 3

// DecodeBlock decodes one received block: body is mode through the suffix
// (parity bits included, as received), bcs the two block-check bytes.
//
// A block whose check fails is repaired when the damage is within reach of
// the parity bits and the 16-bit check:
//
//   - 1..maxParityErrors characters fail parity: one wrong bit in each
//     (8^n candidates), plus — for a single failing character only — one
//     wrong bit in the BCS;
//   - every character passes parity: one wrong bit in the BCS, or two
//     ADJACENT wrong bits inside one character (parity cannot see an even
//     number of errors; adjacent pairs are the burst a demodulator's noise
//     hit produces).
//
// Two guards keep the 16-bit check from "repairing" garbage into a
// plausible-looking wrong message: the repair must be the ONLY candidate in
// the searched space that validates (a second solution means the check
// cannot tell them apart), and the repaired block must read as ACARS
// (printable header and text). A 16-bit check over n candidates validates a
// wrong one with probability ≈ n/65536 — 0.8% at three parity errors —
// which is why acarsdec-style "first match wins" was not copied
// (TestRepairNeverValidatesGarbage: 70 of 20 000 parity-valid garbage blocks
// validate without the guards, none with them). A REAL block damaged beyond
// the searched model can still, rarely, repair to a wrong printable text;
// Corrected is carried to the log and panel so a repaired block is visible
// as one.
//
// CRCOK is false when no unique repair validates; the fields are still
// filled best-effort from the block as received.
func DecodeBlock(body []byte, bcs [2]byte) Message {
	raw := make([]byte, 0, len(body)+2)
	raw = append(raw, body...)
	raw = append(raw, bcs[0], bcs[1])
	m := Message{RawHex: hex.EncodeToString(raw)}
	if len(body) < headerLen || len(body) > maxBodyLen {
		m.parseFields(body)
		return m
	}
	fixed := append([]byte(nil), body...)
	n, ok := correct(fixed, bcs)
	if ok && n > 0 && !plausible(fixed) {
		ok = false
	}
	m.CRCOK = ok
	if ok {
		m.Corrected = n
		m.parseFields(fixed)
	} else {
		m.parseFields(body)
	}
	return m
}

// correct repairs body in place and reports how many bits it flipped and
// whether the block check now validates (see DecodeBlock for the search).
func correct(body []byte, bcs [2]byte) (int, bool) {
	span := len(body) + 2
	crc := CRC(append(append(make([]byte, 0, span), body...), bcs[0], bcs[1]))
	if crc == 0 {
		// A clean check with parity failures would mean an even number of
		// errors cancelled in the CRC — accept only a fully clean block.
		for _, c := range body {
			if !oddParity(c) {
				return 0, false
			}
		}
		return 0, true
	}
	syn := func(k, i int) uint16 { return syndromeTable[span-1-k][i] }
	// bcsBit returns the bit index (0..15) of a lone BCS-bit syndrome, or -1.
	bcsBit := func(s uint16) int {
		for d := 0; d < 2; d++ {
			for i := 0; i < 8; i++ {
				if syndromeTable[d][i] == s {
					return d*8 + i
				}
			}
		}
		return -1
	}

	var bad []int
	for k, c := range body {
		if !oddParity(c) {
			bad = append(bad, k)
		}
	}
	type fix struct {
		flips [][2]int // (char index, bitmask)
		bits  int
	}
	var found []fix
	add := func(f fix) bool {
		found = append(found, f)
		return len(found) > 1 // stop once ambiguous
	}

	switch {
	case len(bad) == 0:
		if bcsBit(crc) >= 0 {
			if add(fix{bits: 1}) {
				return 0, false
			}
		}
		for k := range body {
			for i := 0; i < 7; i++ {
				if syn(k, i)^syn(k, i+1) == crc {
					if add(fix{flips: [][2]int{{k, 3 << i}}, bits: 2}) {
						return 0, false
					}
				}
			}
		}
	case len(bad) <= maxParityErrors:
		choice := make([]int, len(bad))
		for {
			s := crc
			for x, k := range bad {
				s ^= syn(k, choice[x])
			}
			extra := -1
			if s != 0 && len(bad) == 1 {
				extra = bcsBit(s)
			}
			if s == 0 || extra >= 0 {
				f := fix{bits: len(bad)}
				for x, k := range bad {
					f.flips = append(f.flips, [2]int{k, 1 << choice[x]})
				}
				if extra >= 0 {
					f.bits++
				}
				if add(f) {
					return 0, false
				}
			}
			x := 0
			for x < len(choice) {
				choice[x]++
				if choice[x] < 8 {
					break
				}
				choice[x] = 0
				x++
			}
			if x == len(choice) {
				break
			}
		}
	}
	if len(found) != 1 {
		return 0, false
	}
	for _, fl := range found[0].flips {
		body[fl[0]] ^= byte(fl[1])
	}
	return found[0].bits, true
}

// plausible reports whether a repaired block reads as ACARS: printable
// header characters (the label's second character may be DEL, the ack may
// be NAK) and printable text (CR / LF / TAB allowed). It is applied only to
// REPAIRED blocks, as a second witness beside the 16-bit check.
func plausible(body []byte) bool {
	pr := func(c byte) bool { c &= 0x7F; return c >= 0x20 && c < 0x7F }
	for k, c := range body[:headerLen-1] {
		switch {
		case pr(c):
		case k == 8 && c&0x7F == charNAK:
		case k == 10 && c&0x7F == charDEL:
		default:
			return false
		}
	}
	if c := body[headerLen-1]; c != charSTX && c != charETX {
		return false
	}
	text := body[headerLen:]
	if len(text) > 0 {
		if s := text[len(text)-1]; s != charETX && s != charETB {
			return false
		}
		text = text[:len(text)-1]
	}
	for _, c := range text {
		switch c & 0x7F {
		case '\r', '\n', '\t':
		default:
			if !pr(c) {
				return false
			}
		}
	}
	return true
}

// parseFields fills the message fields from a block body (parity bits
// stripped here).
func (m *Message) parseFields(body []byte) {
	c := make([]byte, len(body))
	for i, b := range body {
		c[i] = b & 0x7F
	}
	if len(c) < headerLen {
		return
	}
	m.Mode = c[0]
	m.Address = strings.TrimLeft(string(c[1:8]), ".")
	m.Ack = c[8]
	label := []byte{c[9], c[10]}
	if label[1] == charDEL {
		label[1] = 'd'
	}
	m.Label = string(label)
	m.BlockID = c[11]
	m.Downlink = m.BlockID >= '0' && m.BlockID <= '9'
	if c[12] != charSTX { // ETX in the STX slot: no text
		m.More = false
		return
	}
	text := c[headerLen:]
	if n := len(text); n > 0 {
		switch text[n-1] {
		case charETB & 0x7F:
			m.More = true
			text = text[:n-1]
		case charETX & 0x7F:
			text = text[:n-1]
		}
	}
	if m.Downlink {
		if len(text) >= 4 {
			m.MsgNo, text = string(text[:4]), text[4:]
			if len(text) >= 6 {
				m.FlightID, text = string(text[:6]), text[6:]
			}
		}
	}
	m.Text = string(text)
}
