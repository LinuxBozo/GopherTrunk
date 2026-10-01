// Package receiver is the DSP front end for ACARS (issue #1231): AM IQ (or
// already AM-demodulated audio) becomes 2400-baud MSK audio, then a data-bit
// stream, then framed blocks via internal/radio/acars.Framer:
//
//	IQ chunks (Fs Hz, complex64)
//	  → AM envelope |x|                               [skipped for audio input]
//	  → real resampler (internal/dsp.RealResampler) to AudioRateHz
//	  → coherent MSK demodulator (msk.go): mix by the 1800 Hz centre,
//	    timing from the one-bit phase difference, data from the absolute
//	    phase at each bit boundary
//	  → acars.Framer.Push(bit) → parity / block check / repair
//
// The line code: one cycle of 2400 Hz when a data bit EQUALS the previous
// one, half a cycle of 1200 Hz when it changes (MSK, h = 0.5) — so the
// pre-key's run of ones is a steady 2400 Hz tone. That is MDC1200's
// construction with the tones swapped, and it was not taken on trust: of
// the four tone/line-code mappings, acarsdec decodes ONLY this one when fed
// audio from SynthAudio, and this front end decodes acarsdec's own real-air
// recording to the same seven messages acarsdec prints.
//
// Why not the FFSK tone discriminator the MDC1200 / FleetSync front ends
// use: deciding tones means integrating them back into data, and one wrong
// tone then inverts every later bit of the block. That was measured — the
// tone path decoded 5 of the recording's 7 messages, and one failure was a
// single tone error at byte 11 that complemented the remaining 80 bytes.
// The coherent detector reads each bit from the phase, so an error stays
// one bit that parity and the block check can repair; it decodes all 7, and
// with white noise added to the recording it decodes as many or more
// messages than acarsdec at every level tried.
//
// An envelope detector needs no carrier recovery: |x| is the same whatever
// the residual carrier offset inside the channel filter, so a few kHz of
// tuning or ppm error costs nothing here (unlike the voice chain, #1227).
package receiver

import (
	"errors"
	"fmt"
	"log/slog"
	"math"
	"sync/atomic"
	"time"

	"github.com/MattCheramie/GopherTrunk/internal/dsp"
	"github.com/MattCheramie/GopherTrunk/internal/events"
	"github.com/MattCheramie/GopherTrunk/internal/radio/acars"
	"github.com/MattCheramie/GopherTrunk/internal/storage"
)

// Tone frequencies of the ACARS MSK audio.
const (
	SameHz   = 2400.0 // data bit equals the previous one
	ChangeHz = 1200.0 // data bit differs from the previous one
)

// Oversample is the number of audio samples per bit the MSK demodulator
// runs at.
const Oversample = 8

// AudioRateHz is the internal discriminator rate (baud × Oversample).
const AudioRateHz = acars.BaudHz * Oversample

// Options configures a Receiver.
type Options struct {
	// InputRateHz is the sample rate of the IQ (ProcessIQ) or AM audio
	// (ProcessAudio) stream. Required.
	InputRateHz uint32

	// OnMessage receives every framed block, including check-failed ones
	// (Message.CRCOK false). At least one of OnMessage and Bus is required.
	OnMessage func(acars.Message)

	// Bus, when non-nil, receives every framed block as an
	// events.KindACARSMessage carrying a storage.ACARSMessage.
	Bus *events.Bus

	// DropBadCRC keeps check-failed blocks off the Bus (OnMessage still
	// sees them).
	DropBadCRC bool

	// SourceName, Serial and FrequencyHz identify the receiver; Serial and
	// FrequencyHz are stamped on every published message.
	SourceName  string
	Serial      string
	FrequencyHz uint32

	// Log is optional; defaults to slog.Default.
	Log *slog.Logger
}

// Receiver runs the ACARS decode pipeline. Not safe for concurrent use.
type Receiver struct {
	inputRate uint32
	source    string
	log       *slog.Logger

	rsmp   *dsp.RealResampler
	msk    *mskDemod
	framer *acars.Framer

	onMessage  func(acars.Message)
	bus        *events.Bus
	dropBadCRC bool
	serial     string
	freqHz     uint32

	envBuf  []float32
	rsmpBuf []float32

	samplesSeen   atomic.Uint64
	bitsEmitted   atomic.Uint64
	msgsPublished atomic.Uint64
	msgsDropped   atomic.Uint64
}

// New constructs a Receiver.
func New(opts Options) (*Receiver, error) {
	if opts.OnMessage == nil && opts.Bus == nil {
		return nil, errors.New("acars/receiver: OnMessage or Bus is required")
	}
	if opts.InputRateHz == 0 {
		return nil, errors.New("acars/receiver: InputRateHz is required")
	}
	log := opts.Log
	if log == nil {
		log = slog.Default()
	}
	g := gcd(AudioRateHz, opts.InputRateHz)
	L, M := int(AudioRateHz/g), int(opts.InputRateHz/g)
	if L > 1024 || M > 1<<16 {
		return nil, fmt.Errorf("acars/receiver: input rate %d Hz needs an impractical %d/%d resampler", opts.InputRateHz, L, M)
	}
	// Anti-alias prototype sized like the FFSK front ends (~12 taps per
	// unit of decimation, floor 16, cap 512).
	taps := (12*M + L - 1) / L
	if taps < 16 {
		taps = 16
	}
	if taps > 512 {
		taps = 512
	}
	r := &Receiver{
		inputRate:  opts.InputRateHz,
		source:     opts.SourceName,
		log:        log,
		rsmp:       dsp.NewRealResampler(L, M, taps, 7.0),
		msk:        newMSKDemod(AudioRateHz),
		onMessage:  opts.OnMessage,
		bus:        opts.Bus,
		dropBadCRC: opts.DropBadCRC,
		serial:     opts.Serial,
		freqHz:     opts.FrequencyHz,
	}
	r.framer = acars.NewFramer(r.onFrame)
	return r, nil
}

func (r *Receiver) onFrame(m acars.Message) {
	if r.onMessage != nil {
		r.onMessage(m)
	}
	if r.bus == nil {
		return
	}
	if !m.CRCOK && r.dropBadCRC {
		r.msgsDropped.Add(1)
		return
	}
	now := time.Now()
	msg := MessageToStorage(m, now)
	msg.Serial, msg.FrequencyHz = r.serial, r.freqHz
	r.bus.Publish(events.Event{Kind: events.KindACARSMessage, Timestamp: now, Payload: msg})
	r.msgsPublished.Add(1)
}

// MessageToStorage converts a decoded block into the persisted / wire shape
// the bus carries.
func MessageToStorage(m acars.Message, at time.Time) storage.ACARSMessage {
	return storage.ACARSMessage{
		ReceivedAt: at,
		Mode:       byteString(m.Mode),
		Address:    m.Address,
		Ack:        m.AckString(),
		Label:      m.Label,
		BlockID:    byteString(m.BlockID),
		Downlink:   m.Downlink,
		MsgNo:      m.MsgNo,
		FlightID:   m.FlightID,
		Text:       m.Text,
		More:       m.More,
		CRCOK:      m.CRCOK,
		Corrected:  m.Corrected,
		RawHex:     m.RawHex,
	}
}

func byteString(c byte) string {
	if c < 0x20 || c >= 0x7F {
		return ""
	}
	return string(rune(c))
}

// ProcessIQ runs one chunk of channel IQ (an AM channel) through the chain.
func (r *Receiver) ProcessIQ(chunk []complex64) {
	r.samplesSeen.Add(uint64(len(chunk)))
	if cap(r.envBuf) < len(chunk) {
		r.envBuf = make([]float32, len(chunk))
	}
	env := r.envBuf[:len(chunk)]
	for i, x := range chunk {
		re, im := float64(real(x)), float64(imag(x))
		env[i] = float32(math.Sqrt(re*re + im*im))
	}
	r.processAudio(env)
}

// ProcessAudio runs one chunk of AM-demodulated audio (rtl_fm -M am, an
// SDR's AM audio tap, a recording) at InputRateHz through the chain.
func (r *Receiver) ProcessAudio(chunk []float32) {
	r.samplesSeen.Add(uint64(len(chunk)))
	r.processAudio(chunk)
}

func (r *Receiver) processAudio(audio []float32) {
	r.rsmpBuf = r.rsmp.Process(r.rsmpBuf, audio)
	r.msk.process(r.rsmpBuf, r.pushSoft)
}

func (r *Receiver) pushSoft(soft float64) {
	var bit byte
	if soft < 0 {
		bit = 1
	}
	r.framer.Push(bit)
	r.msk.tracking = r.framer.Busy()
	r.bitsEmitted.Add(1)
}

// Busy reports whether a block is part-way through framing — the scanner
// holds the channel while it is.
func (r *Receiver) Busy() bool { return r.framer.Busy() }

// Reset clears every stage, the framer included, so a block cut off by a
// retune is abandoned rather than completed from the next channel's bits.
func (r *Receiver) Reset() {
	r.rsmp.Reset()
	r.msk.reset()
	r.framer.Reset()
}

// Framer exposes the protocol framer for its Stats.
func (r *Receiver) Framer() *acars.Framer { return r.framer }

// Stats are cumulative front-end counters.
type Stats struct {
	SamplesSeen       uint64
	BitsEmitted       uint64
	MessagesPublished uint64
	MessagesDropped   uint64
}

// Stats returns the cumulative counters.
func (r *Receiver) Stats() Stats {
	return Stats{
		SamplesSeen:       r.samplesSeen.Load(),
		BitsEmitted:       r.bitsEmitted.Load(),
		MessagesPublished: r.msgsPublished.Load(),
		MessagesDropped:   r.msgsDropped.Load(),
	}
}

func gcd(a, b uint32) uint32 {
	for b != 0 {
		a, b = b, a%b
	}
	return a
}
