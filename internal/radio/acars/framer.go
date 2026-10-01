package acars

import "math/bits"

// Framer turns a demodulated ACARS data-bit stream into decoded blocks. It
// hunts the sync sequence that follows the pre-key, collects characters up
// to the ETX/ETB suffix, reads the two block-check bytes, and hands the
// block to DecodeBlock.
//
// The bits it is fed are DATA bits (the receiver has already undone the MSK
// line code), one per Push, in wire order. Characters arrive LSB first.
//
// Polarity: the receiver recovers data as a running XOR of tone decisions
// (see internal/radio/acars/receiver), whose start state is unknown, so the
// hunt accepts the sync sequence and its bitwise complement and inverts the
// block's bits when it locks on the complement — the approach the MDC1200
// and FleetSync framers use for the same ambiguity.
type Framer struct {
	onMsg func(Message)

	reg      uint64 // last huntBits bits, newest in the top bit
	locked   bool
	inverted bool
	cur      byte // character being assembled, LSB first
	nbits    int
	body     []byte // mode through suffix
	inBCS    bool   // suffix seen: the next two characters are the BCS
	bcs      [2]byte
	nbcs     int

	stats Stats
}

// Stats are cumulative framer counters.
type Stats struct {
	SyncLocks uint64 // sync sequences found
	Blocks    uint64 // blocks handed to OnMessage
	CRCOK     uint64 // blocks whose check validated (after any correction)
	Corrected uint64 // validated blocks that needed correction
	Aborted   uint64 // locks abandoned (no suffix within MaxTextLen)
}

const (
	// huntBits is the sync window: the last pre-key character plus
	// '+' '*' SYN SYN SOH — 48 bits.
	huntBits = 48
	huntMask = uint64(1)<<huntBits - 1
	// syncMaxErrors tolerates sliced-bit errors in the 48-bit window. The
	// block check is the real gate; at 2 errors a random bit stream
	// matches (1+48+1128)/2^48 ≈ 4e-12 per bit per polarity.
	syncMaxErrors = 2
)

// syncPattern is the hunt window as it sits in reg once its last bit has
// arrived: character i of 0xFF '+' '*' SYN SYN SOH in bits 8i..8i+7, each
// character LSB first.
var syncPattern = func() uint64 {
	seq := []byte{charPreKey, charPlus, charStar, charSYN, charSYN, charSOH}
	var p uint64
	for i, c := range seq {
		p |= uint64(c) << (8 * i)
	}
	return p
}()

// NewFramer constructs a Framer; onMsg receives every block that framed,
// including ones whose check failed (Message.CRCOK false). It must not be
// nil.
func NewFramer(onMsg func(Message)) *Framer {
	if onMsg == nil {
		panic("acars: OnMessage callback is required")
	}
	return &Framer{onMsg: onMsg, body: make([]byte, 0, maxBodyLen)}
}

// Push feeds one data bit (only bit 0 is read).
func (f *Framer) Push(bit byte) {
	bit &= 1
	if !f.locked {
		f.reg = (f.reg>>1 | uint64(bit)<<(huntBits-1)) & huntMask
		switch {
		case bits.OnesCount64(f.reg^syncPattern) <= syncMaxErrors:
			f.lock(false)
		case bits.OnesCount64(f.reg^(^syncPattern&huntMask)) <= syncMaxErrors:
			f.lock(true)
		}
		return
	}
	if f.inverted {
		bit ^= 1
	}
	f.cur = f.cur>>1 | bit<<7
	f.nbits++
	if f.nbits < 8 {
		return
	}
	c := f.cur
	f.cur, f.nbits = 0, 0
	f.char(c)
}

func (f *Framer) lock(inverted bool) {
	f.locked, f.inverted, f.inBCS = true, inverted, false
	f.cur, f.nbits, f.nbcs = 0, 0, 0
	f.body = f.body[:0]
	f.stats.SyncLocks++
}

// char consumes one received character after the sync.
func (f *Framer) char(c byte) {
	if f.inBCS {
		f.bcs[f.nbcs] = c
		f.nbcs++
		if f.nbcs == 2 {
			f.emit(f.body, f.bcs)
		}
		return
	}
	f.body = append(f.body, c)
	n := len(f.body)
	switch {
	case (c == charETX || c == charETB) && n >= headerLen:
		// The suffix: the BCS follows. (An ETX in the STX slot of a
		// text-less block is the suffix too: n == headerLen.)
		f.inBCS = true
	case c == charDEL && n >= headerLen+3:
		// The block-ending DEL arrived but no suffix was seen: a bit error
		// hit the ETX/ETB. The three characters before DEL are suffix +
		// BCS; hand that to the corrector, which can repair the suffix.
		body := f.body[:n-3]
		bcs := [2]byte{f.body[n-3], f.body[n-2]}
		f.emit(body, bcs)
	case n >= maxBodyLen:
		f.stats.Aborted++
		f.unlock()
	}
}

func (f *Framer) emit(body []byte, bcs [2]byte) {
	m := DecodeBlock(body, bcs)
	f.stats.Blocks++
	if m.CRCOK {
		f.stats.CRCOK++
		if m.Corrected > 0 {
			f.stats.Corrected++
		}
	}
	f.unlock()
	f.onMsg(m)
}

func (f *Framer) unlock() {
	f.locked, f.inverted, f.inBCS = false, false, false
	f.reg = 0
	f.cur, f.nbits, f.nbcs = 0, 0, 0
	f.body = f.body[:0]
}

// Busy reports whether a block is part-way through framing.
func (f *Framer) Busy() bool { return f.locked }

// Reset abandons any block in progress and clears the hunt register.
func (f *Framer) Reset() { f.unlock() }

// Stats returns the cumulative counters.
func (f *Framer) Stats() Stats { return f.stats }
