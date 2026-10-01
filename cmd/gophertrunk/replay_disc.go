package main

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"math/cmplx"
	"os"
	"sort"

	"github.com/MattCheramie/GopherTrunk/internal/siglab"
	"github.com/MattCheramie/GopherTrunk/internal/voice"
)

// `replay -format disc` (issue #1187): decode a DISCRIMINATOR-AUDIO recording
// — the FM demodulator's raw, unsquelched output, the form DSD+, DSD-FME and
// the #1187 reporter's captures take — instead of IQ. The audio is turned
// back into IQ by treating each sample as an instantaneous frequency
// (±discDefaultDeviationHz at full scale) and integrating the phase, so the
// production receiver's own discriminator recovers the same waveform; the
// result goes through the normal decode (and -record-voice / -key) path.
// This is the conversion the DMR Enhanced Privacy and P25 ADP harnesses
// verified the #1187 captures with, promoted so the release binary can do it.

// discDefaultDeviationHz is the full-scale deviation the remodulation
// assumes. Its absolute value barely matters — the C4FM/4FSK receivers
// normalise their symbol levels — so long as the loudest sample stays well
// inside ±fs/2; 4800 Hz is the value the #1187 captures decoded at.
const discDefaultDeviationHz = 4800.0

// discAmplitude is the envelope of the remodulated IQ (see
// remodulateDiscriminatorAudio): −6 dBFS, clear of the overload check.
const discAmplitude = 0.5

// remodulateDiscriminatorAudio turns discriminator audio (a sample of ±1.0
// is ±dev Hz) into constant-envelope IQ at the same rate. The envelope is
// discAmplitude, not 1: siglab's front-end overload check counts samples at
// the ±1 rail, and unit-amplitude IQ read as "25 % of samples clipped — the
// capture is overloaded", a false diagnosis to hand an operator. An FM
// receiver does not care about the amplitude.
func remodulateDiscriminatorAudio(samples []float32, rate, dev float64) []complex64 {
	iq := make([]complex64, len(samples))
	var phase float64
	for i, s := range samples {
		phase += 2 * math.Pi * float64(s) * dev / rate
		if phase > math.Pi {
			phase -= 2 * math.Pi
		} else if phase < -math.Pi {
			phase += 2 * math.Pi
		}
		iq[i] = complex64(cmplx.Rect(discAmplitude, phase))
	}
	return iq
}

// discriminatorAudioSanity characterises a discriminator-audio recording
// before the receiver is blamed for not decoding it. A demodulated 4800-baud
// 4FSK / C4FM signal keeps ~all of its energy below 3 kHz, so audio whose
// energy sits mostly ABOVE 3 kHz is not a discriminator tap of that signal —
// the #1187 reporter's first DMR files were decoded/garbled audio re-recorded
// at 96 kHz (white-noise bursts to 15.5 kHz), and no receiver decodes
// anything from those.
//
// It is measured per 20 ms block, not over the whole file: a squelch-off
// discriminator recording spends most of its time on receiver noise between
// transmissions, which is wideband by nature, and a whole-file figure called
// a genuine tap "not a tap" (measured: 47 % on a recording whose keyed
// stretches read 93 %). Blocks near digital silence are ignored, and the
// verdict uses the 95th percentile of the per-block fraction — the most
// signal-like stretches — so any real transmission in the file is seen.
func discriminatorAudioSanity(samples []float32, rate float64, modulation string) string {
	if len(samples) < 1024 || rate <= 6000 {
		return ""
	}
	// Bilinear-transformed 2nd-order Butterworth low-pass, corner 3 kHz.
	k := math.Tan(math.Pi * 3000 / rate)
	norm := 1 / (1 + math.Sqrt2*k + k*k)
	b0 := k * k * norm
	b1 := 2 * b0
	b2 := b0
	a1 := 2 * (k*k - 1) * norm
	a2 := (1 - math.Sqrt2*k + k*k) * norm
	block := int(rate * 0.02)
	type blk struct{ low, all float64 }
	var (
		blocks         []blk
		cur            blk
		x1, x2, y1, y2 float64
		maxAll         float64
	)
	for i, s := range samples {
		x := float64(s)
		y := b0*x + b1*x1 + b2*x2 - a1*y1 - a2*y2
		x2, x1 = x1, x
		y2, y1 = y1, y
		cur.low += y * y
		cur.all += x * x
		if (i+1)%block == 0 {
			blocks = append(blocks, cur)
			maxAll = math.Max(maxAll, cur.all)
			cur = blk{}
		}
	}
	if maxAll == 0 {
		return "audio is digital silence"
	}
	var fracs []float64
	for _, b := range blocks {
		if b.all >= 1e-3*maxAll {
			fracs = append(fracs, b.low/b.all)
		}
	}
	sort.Float64s(fracs)
	frac := fracs[int(0.95*float64(len(fracs)-1))]
	verdict := "consistent with a discriminator tap"
	if frac < 0.6 {
		verdict = fmt.Sprintf("NOT a demodulable %s discriminator tap — a 4800-baud %s signal keeps ~90%% of its energy below 3 kHz; this looks like decoded/garbled audio or a wideband recording, and no receiver can recover bursts from it (record IQ with `gophertrunk capture`, or the receiver's raw unsquelched discriminator output instead)", modulation, modulation)
	}
	return fmt.Sprintf("energy below 3 kHz in its most signal-like 20 ms blocks: %.0f%% (%s)", 100*frac, verdict)
}

// readDiscAudio reads a discriminator-audio recording as float samples in
// [-1, 1] plus its rate. WAV: 8- or 16-bit PCM or 32-bit float, any channel
// count (the first channel is used — an SDR++/SDR# "stereo" discriminator
// recording carries the same signal on both), WAVE_FORMAT_EXTENSIBLE
// unwrapped, chunks before "data" skipped. FLAC: 16-bit mono via the voice
// reader.
func readDiscAudio(path string) ([]float32, float64, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, 0, err
	}
	if bytes.HasPrefix(raw, []byte("fLaC")) {
		s, rate, err := voice.ReadAudioSamples(path)
		if err != nil {
			return nil, 0, err
		}
		out := make([]float32, len(s))
		for i, v := range s {
			out[i] = float32(v) / 32768
		}
		return out, float64(rate), nil
	}
	if len(raw) < 12 || string(raw[0:4]) != "RIFF" || string(raw[8:12]) != "WAVE" {
		return nil, 0, errors.New("not a WAV (RIFF/WAVE) or FLAC file")
	}
	var (
		format, channels, bitsPer uint16
		rate                      uint32
		data                      []byte
		haveFmt                   bool
	)
	for p := 12; p+8 <= len(raw); {
		id, size := string(raw[p:p+4]), int(binary.LittleEndian.Uint32(raw[p+4:]))
		end := p + 8 + size
		if end > len(raw) || size < 0 {
			end = len(raw)
		}
		body := raw[p+8 : end]
		switch id {
		case "fmt ":
			if len(body) < 16 {
				return nil, 0, errors.New("short WAV fmt chunk")
			}
			format = binary.LittleEndian.Uint16(body[0:])
			channels = binary.LittleEndian.Uint16(body[2:])
			rate = binary.LittleEndian.Uint32(body[4:])
			bitsPer = binary.LittleEndian.Uint16(body[14:])
			if format == 0xFFFE && len(body) >= 26 { // WAVE_FORMAT_EXTENSIBLE
				format = binary.LittleEndian.Uint16(body[24:])
			}
			haveFmt = true
		case "data":
			data = body
		}
		p += 8 + size + size&1
	}
	if !haveFmt || data == nil || channels == 0 || rate == 0 {
		return nil, 0, errors.New("WAV is missing its fmt or data chunk")
	}
	width := int(bitsPer) / 8
	if width == 0 {
		return nil, 0, fmt.Errorf("unsupported WAV sample width %d bits", bitsPer)
	}
	frame := int(channels) * width
	n := len(data) / frame
	out := make([]float32, n)
	for i := 0; i < n; i++ {
		off := i * frame // first channel
		switch {
		case format == 1 && bitsPer == 16:
			out[i] = float32(int16(binary.LittleEndian.Uint16(data[off:]))) / 32768
		case format == 1 && bitsPer == 8:
			out[i] = (float32(data[off]) - 128) / 128
		case format == 3 && bitsPer == 32:
			out[i] = math.Float32frombits(binary.LittleEndian.Uint32(data[off:]))
		default:
			return nil, 0, fmt.Errorf("unsupported WAV encoding (format %d, %d bits): use 8/16-bit PCM or 32-bit float", format, bitsPer)
		}
	}
	return out, float64(rate), nil
}

// prepareDiscInput converts a discriminator-audio file into a temporary f32
// IQ file at the audio's own rate for the normal decode path. It returns the
// IQ path, the rate, the sanity line and a cleanup func.
func prepareDiscInput(path string, dev float64) (iqPath string, rate float64, sanity string, cleanup func(), err error) {
	samples, rate, err := readDiscAudio(path)
	if err != nil {
		return "", 0, "", nil, fmt.Errorf("-format disc: %s: %w", path, err)
	}
	if len(samples) == 0 {
		return "", 0, "", nil, fmt.Errorf("-format disc: %s: no samples", path)
	}
	if dev <= 0 || dev >= rate/2 {
		return "", 0, "", nil, fmt.Errorf("-disc-dev %.0f Hz must be > 0 and below half the audio rate (%.0f Hz)", dev, rate/2)
	}
	iq := remodulateDiscriminatorAudio(samples, rate, dev)
	f, err := os.CreateTemp("", "gophertrunk-disc-*.cf32")
	if err != nil {
		return "", 0, "", nil, err
	}
	if _, err := f.Write(siglab.EncodeCapture(iq, siglab.FormatF32)); err != nil {
		f.Close()
		os.Remove(f.Name())
		return "", 0, "", nil, err
	}
	if err := f.Close(); err != nil {
		os.Remove(f.Name())
		return "", 0, "", nil, err
	}
	name := f.Name()
	return name, rate, discriminatorAudioSanity(samples, rate, "4FSK / C4FM"), func() { os.Remove(name) }, nil
}
