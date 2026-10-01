package acars

// Synthesis helpers. They exist so tests and the replay harness can build a
// known-good transmission; they are NOT the source of truth for the wire
// format (that is the real-air vectors in the tests, and acarsdec decoding
// this encoder's audio — see the package comment).

// Block is the content of one block to synthesise. Text is the full text
// field — for a downlink that starts with the 4-char message number and the
// 6-char flight id.
type Block struct {
	Mode    byte
	Address string // up to 7 chars; left-padded with '.'
	Ack     byte
	Label   string // 2 chars
	BlockID byte
	Text    string
	More    bool // ETB suffix instead of ETX
}

// PreKeyChars is how many 0xFF pre-key characters EncodeBlock emits.
const PreKeyChars = 16

// EncodeBlock returns the block's wire characters: pre-key, sync, SOH,
// body with parity, BCS (low byte first) and the trailing DEL.
func EncodeBlock(b Block) []byte {
	out := make([]byte, 0, PreKeyChars+5+headerLen+len(b.Text)+4)
	for i := 0; i < PreKeyChars; i++ {
		out = append(out, charPreKey)
	}
	out = append(out, charPlus, charStar, charSYN, charSYN, charSOH)
	body := BlockBody(b)
	out = append(out, body...)
	crc := CRC(body)
	out = append(out, byte(crc), byte(crc>>8), charDEL)
	return out
}

// BlockBody returns mode through the suffix, parity applied.
func BlockBody(b Block) []byte {
	addr := b.Address
	for len(addr) < 7 {
		addr = "." + addr
	}
	label := b.Label
	for len(label) < 2 {
		label += " "
	}
	body := []byte{withParity(b.Mode)}
	for i := 0; i < 7; i++ {
		body = append(body, withParity(addr[i]))
	}
	body = append(body, withParity(b.Ack), withParity(label[0]), withParity(label[1]), withParity(b.BlockID))
	suffix := byte(charETX)
	if b.More {
		suffix = charETB
	}
	if b.Text == "" && !b.More {
		return append(body, charETX)
	}
	body = append(body, charSTX)
	for i := 0; i < len(b.Text); i++ {
		body = append(body, withParity(b.Text[i]))
	}
	return append(body, suffix)
}

// Bits expands characters into data bits, LSB first.
func Bits(chars []byte) []byte {
	out := make([]byte, 0, 8*len(chars))
	for _, c := range chars {
		for i := 0; i < 8; i++ {
			out = append(out, c>>i&1)
		}
	}
	return out
}
