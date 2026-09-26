package receiver

import (
	"testing"
	"time"

	"github.com/MattCheramie/GopherTrunk/internal/events"
	"github.com/MattCheramie/GopherTrunk/internal/radio/mdc1200"
	"github.com/MattCheramie/GopherTrunk/internal/storage"
)

// crc16 mirrors the package-internal MDC1200 CRC so tests can build
// CRC-valid frames without exporting the implementation.
func crc16(data []byte) uint16 {
	crc := uint16(0x0000)
	for _, b := range data {
		crc ^= uint16(b)
		for i := 0; i < 8; i++ {
			if crc&1 != 0 {
				crc = (crc >> 1) ^ 0x8408
			} else {
				crc >>= 1
			}
		}
	}
	return ^crc
}

// encodeFrame is the inverse of mdc1200.deinterleave (see the parser
// test for the matching helper): pack 14 bytes into logical bit order,
// then apply the 16×7 column interleave to get the 112 wire bits.
func encodeFrame(data [14]byte) []byte {
	var lbits [mdc1200.FrameBits]byte
	for i := 0; i < 14; i++ {
		for j := 0; j < 8; j++ {
			if data[i]&(1<<uint(j)) != 0 {
				lbits[i*8+j] = 1
			}
		}
	}
	bits := make([]byte, mdc1200.FrameBits)
	idx := 0
	for i := 0; i < 16; i++ {
		for j := 0; j < 7; j++ {
			bits[j*16+i] = lbits[idx]
			idx++
		}
	}
	return bits
}

func frameBits(op, arg uint8, unitID uint16) []byte {
	var data [14]byte
	data[0] = op
	data[1] = arg
	data[2] = byte(unitID >> 8)
	data[3] = byte(unitID)
	crc := crc16(data[:4])
	data[4] = byte(crc)
	data[5] = byte(crc >> 8)
	return encodeFrame(data)
}

// syncBits emits the 40-bit sync word most-significant bit first.
func syncBits() []byte {
	out := make([]byte, mdc1200.SyncBits)
	for i := 0; i < mdc1200.SyncBits; i++ {
		shift := uint(mdc1200.SyncBits - 1 - i)
		out[i] = byte((mdc1200.SyncWord >> shift) & 1)
	}
	return out
}

// burst is preamble dotting + sync + payload, the full on-air bit
// sequence the framer expects.
func burst(op, arg uint8, unitID uint16) []byte {
	var bits []byte
	for i := 0; i < 24; i++ { // dotting preamble (alternating)
		bits = append(bits, byte(i&1))
	}
	bits = append(bits, syncBits()...)
	bits = append(bits, frameBits(op, arg, unitID)...)
	return bits
}

func newTestBus(t *testing.T) (*events.Bus, *events.Subscription) {
	t.Helper()
	bus := events.NewBus(8)
	sub := bus.Subscribe()
	t.Cleanup(func() {
		sub.Close()
		bus.Close()
	})
	return bus, sub
}

func waitMsg(t *testing.T, sub *events.Subscription) (storage.MDC1200Message, bool) {
	t.Helper()
	deadline := time.After(time.Second)
	for {
		select {
		case ev, ok := <-sub.C:
			if !ok {
				return storage.MDC1200Message{}, false
			}
			if ev.Kind != events.KindMDC1200Message {
				continue
			}
			msg, _ := ev.Payload.(storage.MDC1200Message)
			return msg, true
		case <-deadline:
			return storage.MDC1200Message{}, false
		}
	}
}

func TestNewRequiresBus(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("New without Bus: want panic")
		}
	}()
	New(Options{})
}

func TestReceiverDecodesBurst(t *testing.T) {
	bus, sub := newTestBus(t)
	r := New(Options{Bus: bus})
	for _, b := range burst(0x01, 0x80, 0x1234) {
		r.Push(b)
	}
	msg, ok := waitMsg(t, sub)
	if !ok {
		t.Fatalf("no burst emitted (stats=%+v)", r.Stats())
	}
	if msg.UnitID != 0x1234 {
		t.Errorf("UnitID = 0x%04X, want 0x1234", msg.UnitID)
	}
	if msg.Operation != "PTT ID" {
		t.Errorf("Operation = %q, want %q", msg.Operation, "PTT ID")
	}
	if !msg.CRCOK {
		t.Errorf("CRCOK = false, want true")
	}
	if got := r.Stats().BurstsEmitted; got != 1 {
		t.Errorf("BurstsEmitted = %d, want 1", got)
	}
}

func TestReceiverDecodesInvertedBurst(t *testing.T) {
	bus, sub := newTestBus(t)
	r := New(Options{Bus: bus})
	for _, b := range burst(0x00, 0x90, 0x0042) {
		r.Push(b ^ 1) // invert the whole stream (flipped FM discriminator)
	}
	msg, ok := waitMsg(t, sub)
	if !ok {
		t.Fatalf("no burst emitted for inverted stream (stats=%+v)", r.Stats())
	}
	if msg.UnitID != 0x0042 || msg.Operation != "Emergency" {
		t.Errorf("got unit=0x%04X op=%q, want 0x0042/Emergency", msg.UnitID, msg.Operation)
	}
}

func TestReceiverDropBadCRC(t *testing.T) {
	bus, sub := newTestBus(t)
	r := New(Options{Bus: bus, DropBadCRC: true})
	b := burst(0x01, 0x80, 0x1234)
	// Corrupt bits inside the CRC-protected header. Payload starts at
	// offset 24 (preamble) + 40 (sync) = 64; wire position j*16+i maps
	// to logical bit i*7+j, so payload offset 0 is data[0] bit 0.
	payload := 24 + mdc1200.SyncBits
	b[payload+0] ^= 1  // data[0] bit 0 (op)
	b[payload+34] ^= 1 // data[2] region (unit-ID high)
	for _, bit := range b {
		r.Push(bit)
	}
	if _, ok := waitMsg(t, sub); ok {
		t.Errorf("burst emitted despite DropBadCRC + corrupted CRC")
	}
}

func TestReceiverDoublePacket(t *testing.T) {
	bus, sub := newTestBus(t)
	r := New(Options{Bus: bus})
	var bits []byte
	bits = append(bits, burst(0x35, 0x00, 0x0777)...) // op 0x35 → double
	bits = append(bits, frameBits(0x00, 0x00, 0x0000)...)
	for _, b := range bits {
		r.Push(b)
	}
	msg, ok := waitMsg(t, sub)
	if !ok {
		t.Fatalf("no burst emitted for double packet (stats=%+v)", r.Stats())
	}
	if msg.UnitID != 0x0777 {
		t.Errorf("UnitID = 0x%04X, want 0x0777", msg.UnitID)
	}
	if r.Stats().BurstsEmitted != 1 {
		t.Errorf("BurstsEmitted = %d, want 1 (double packet → single event)", r.Stats().BurstsEmitted)
	}
}

// Busy is true from the sync lock to the end of the burst, and every
// published message carries the configured channel identity (#1220).
func TestReceiverBusyAndChannelIdentity(t *testing.T) {
	bus, sub := newTestBus(t)
	r := New(Options{Bus: bus, Serial: "R2", FrequencyHz: 146_670_000})
	b := burst(0x01, 0x80, 0x1234)
	lock := 24 + mdc1200.SyncBits // bits up to and including the sync word
	for i, bit := range b {
		r.Push(bit)
		switch {
		case i < lock-1 && r.Busy():
			t.Fatalf("Busy before the sync word completed (bit %d)", i)
		case i >= lock-1 && i < len(b)-1 && !r.Busy():
			t.Fatalf("not Busy mid-burst (bit %d)", i)
		}
	}
	if r.Busy() {
		t.Fatal("still Busy after the burst was emitted")
	}
	msg, ok := waitMsg(t, sub)
	if !ok {
		t.Fatal("no burst emitted")
	}
	if msg.Serial != "R2" || msg.FrequencyHz != 146_670_000 {
		t.Errorf("serial=%q freq=%d, want R2 / 146670000", msg.Serial, msg.FrequencyHz)
	}
}

// Reset abandons a burst in progress (#1220 retune).
func TestReceiverResetAbandonsPartialBurst(t *testing.T) {
	bus, _ := newTestBus(t)
	r := New(Options{Bus: bus})
	b := burst(0x01, 0x80, 0x1234)
	half := 24 + mdc1200.SyncBits + mdc1200.FrameBits/2
	for _, bit := range b[:half] {
		r.Push(bit)
	}
	if !r.Busy() {
		t.Fatal("not Busy mid-burst")
	}
	r.Reset()
	if r.Busy() {
		t.Fatal("Busy after Reset")
	}
	for _, bit := range b[half:] {
		r.Push(bit)
	}
	if got := r.Stats().BurstsEmitted; got != 0 {
		t.Fatalf("emitted %d bursts from the abandoned half-burst", got)
	}
}
