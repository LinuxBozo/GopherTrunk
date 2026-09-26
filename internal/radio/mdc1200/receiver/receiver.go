// Package receiver frames a demodulated MDC1200 bit stream: it hunts
// the 40-bit sync word, captures the 112-bit data block(s) that
// follow, hands each block to internal/radio/mdc1200 for decode, and
// publishes one events.KindMDC1200Message per burst.
//
// The DSP layer above this package (internal/radio/mdc1200/afsk:
// 1200-baud FFSK demodulation + NRZ slicing) hands the receiver its
// bit stream. From bits down through bus event is what this package
// owns.
//
// One Receiver instance per MDC1200 channel. The Push(bit) method is
// the single entry point — call it once per NRZ wire bit. Bursts flow
// onto the bus, are persisted by storage.MDC1200Log, reach the REST
// endpoint, and render on the /mdc1200 web panel.
//
// Polarity: an FM discriminator can present the burst with either tone
// sense depending on tuning, so the sync hunt accepts both the sync
// word and its bitwise complement; when it locks on the complement the
// captured payload bits are inverted to recover the true data.
package receiver

import (
	"math/bits"
	"sync/atomic"
	"time"

	"github.com/MattCheramie/GopherTrunk/internal/events"
	"github.com/MattCheramie/GopherTrunk/internal/radio/mdc1200"
	"github.com/MattCheramie/GopherTrunk/internal/storage"
)

// gdThresh is the maximum number of bit errors tolerated across the
// 40-bit sync word before a burst is declared. Matches the threshold
// the reference MDC1200 decoders use.
const gdThresh = 5

// syncMask isolates the low SyncBits of the shift register.
const syncMask = (uint64(1) << mdc1200.SyncBits) - 1

type state uint8

const (
	stateHunt   state = iota // searching for the sync word
	stateBlock1              // collecting the first 112-bit block
	stateBlock2              // collecting the second block (double packet)
)

// Receiver consumes a bit stream and publishes MDC1200 bursts on the
// shared events bus. Zero-value Receivers are NOT usable — call New.
type Receiver struct {
	bus *events.Bus

	// dropBadCRC, when true, silently discards bursts whose CRC didn't
	// match. Defaults to false — the receiver publishes CRC-failed
	// bursts too, with CRCOK=false, so the panel can flag marginal
	// signals.
	dropBadCRC bool

	st       state
	reg      uint64 // sliding 40-bit sync shift register
	inverted bool   // locked on the complemented sync word
	buf      [mdc1200.FrameBits]byte
	n        int             // bits captured into buf
	first    mdc1200.Message // first block of a double packet

	serial string // Options.Serial, stamped on every published message
	freqHz uint32 // Options.FrequencyHz, likewise

	// Counters surfaced for /metrics.
	burstsIn   atomic.Uint64 // sync words detected
	burstsCRC  atomic.Uint64 // bursts that failed CRC
	burstsEmit atomic.Uint64 // events published to the bus
}

// Options configures a Receiver.
type Options struct {
	// Bus is required — bursts publish onto KindMDC1200Message.
	Bus *events.Bus

	// DropBadCRC silently discards CRC-failed bursts when true.
	DropBadCRC bool

	// Serial and FrequencyHz identify the SDR and channel feeding this
	// receiver; both are stamped on every published storage.MDC1200Message
	// so the log, REST endpoint and panel can say which channel produced a
	// burst (#1220 — a conventional scanner hops between channels on one
	// SDR, so the serial alone cannot). Optional.
	Serial      string
	FrequencyHz uint32
}

// New constructs a Receiver. Panics if opts.Bus is nil — receivers
// without a bus have nowhere to publish.
func New(opts Options) *Receiver {
	if opts.Bus == nil {
		panic("mdc1200/receiver: events.Bus is required")
	}
	return &Receiver{bus: opts.Bus, dropBadCRC: opts.DropBadCRC, serial: opts.Serial, freqHz: opts.FrequencyHz}
}

// Push feeds one NRZ wire bit through the framer. Bits outside {0, 1}
// are masked to their low bit.
func (r *Receiver) Push(bit byte) {
	bit &= 1
	switch r.st {
	case stateHunt:
		r.reg = (r.reg << 1) | uint64(bit)
		d := bits.OnesCount64((r.reg ^ mdc1200.SyncWord) & syncMask)
		switch {
		case d <= gdThresh:
			r.beginBlock(false)
		case d >= mdc1200.SyncBits-gdThresh:
			// Matched the complemented sync word — inverted polarity.
			r.beginBlock(true)
		}
	case stateBlock1, stateBlock2:
		if r.inverted {
			bit ^= 1
		}
		r.buf[r.n] = bit
		r.n++
		if r.n == mdc1200.FrameBits {
			r.finishBlock()
		}
	}
}

// beginBlock transitions out of the sync hunt into payload capture.
func (r *Receiver) beginBlock(inverted bool) {
	r.burstsIn.Add(1)
	r.inverted = inverted
	r.n = 0
	r.st = stateBlock1
}

// finishBlock decodes a captured 112-bit block and either publishes a
// burst or, for a double packet, advances to the second block.
func (r *Receiver) finishBlock() {
	msg, _ := mdc1200.DecodeFrame(r.buf[:])

	if r.st == stateBlock1 && msg.DoublePacket {
		// Capture the trailing data block before publishing.
		r.first = msg
		r.n = 0
		r.st = stateBlock2
		return
	}

	if r.st == stateBlock2 {
		// Attach the second block's header bytes and report the first
		// block's operation, which carries the meaningful op/arg/unit.
		first := r.first
		first.Extra = decodeExtra(msg)
		msg = first
	}

	r.emit(msg)
	r.reset()
}

// decodeExtra extracts the raw header bytes of a double packet's
// second block for the Extra field.
func decodeExtra(second mdc1200.Message) []byte {
	if second.RawHex == "" {
		return nil
	}
	return []byte(second.RawHex)
}

// emit publishes a decoded burst, honoring DropBadCRC.
func (r *Receiver) emit(msg mdc1200.Message) {
	if !msg.CRCOK {
		r.burstsCRC.Add(1)
		if r.dropBadCRC {
			return
		}
	}
	r.bus.Publish(events.Event{
		Kind:      events.KindMDC1200Message,
		Timestamp: time.Now(),
		Payload: storage.MDC1200Message{
			Op:          msg.Op,
			Arg:         msg.Arg,
			UnitID:      msg.UnitID,
			Operation:   msg.Operation,
			Body:        msg.Body,
			RawHex:      msg.RawHex,
			CRCOK:       msg.CRCOK,
			Serial:      r.serial,
			FrequencyHz: r.freqHz,
		},
	})
	r.burstsEmit.Add(1)
}

// Busy reports whether the framer has locked a sync word and is part-way
// through capturing a burst (either block of a double packet). A caller
// that owns the RF channel (the conventional scanner's dwell, #1220) holds
// the channel while this is true, so a burst is not cut off mid-frame.
func (r *Receiver) Busy() bool { return r.st != stateHunt }

// Reset abandons any burst in progress and returns to the sync hunt. A
// front end calls it on a retune so a burst cut off on one channel cannot
// swallow the next channel's bits as its payload.
func (r *Receiver) Reset() { r.reset() }

// reset returns the framer to the sync hunt with a cleared shift
// register so the just-decoded burst can't immediately re-trigger.
func (r *Receiver) reset() {
	r.st = stateHunt
	r.reg = 0
	r.n = 0
	r.inverted = false
}

// Stats reports cumulative counters for /metrics + debugging.
type Stats struct {
	BurstsIn      uint64 // sync words detected
	BurstsBadCRC  uint64 // bursts that failed the CRC check
	BurstsEmitted uint64 // events published to the bus
}

func (r *Receiver) Stats() Stats {
	return Stats{
		BurstsIn:      r.burstsIn.Load(),
		BurstsBadCRC:  r.burstsCRC.Load(),
		BurstsEmitted: r.burstsEmit.Load(),
	}
}
