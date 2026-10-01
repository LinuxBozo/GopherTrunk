package main

import (
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"strconv"
	"testing"

	"github.com/MattCheramie/GopherTrunk/internal/radio/acars"
	acarsrx "github.com/MattCheramie/GopherTrunk/internal/radio/acars/receiver"
	"github.com/MattCheramie/GopherTrunk/internal/scanner/ccdecoder"
)

// TestACARSReplay is the on-air verification gate for the ACARS decoder
// (#1231). It replays a real capture through the PRODUCTION front end
// (acars/receiver: AM envelope → MSK tone discriminator → symbol timing →
// running XOR → framer → block check) and prints every block it frames.
// Skip-guarded; one of:
//
//	GT_ACARS_AUDIO=<wav> go test ./cmd/gophertrunk -run TestACARSReplay -v
//	GT_ACARS_IQ=<capture> GT_ACARS_RATE=<Hz> go test ./cmd/gophertrunk -run TestACARSReplay -v
//
// GT_ACARS_AUDIO is AM-DEMODULATED audio — what `rtl_fm -M am` writes, or a
// receiver's AM audio tap — as a WAV (16-bit PCM, 8-bit or float32, any
// channel count; every channel is decoded in turn, as acarsdec does with a
// multi-channel file). This is the form the #1231 reporter's acarsdec
// cross-check took.
//
// GT_ACARS_IQ is raw IQ (wav/flac containers are content-sniffed; headerless
// files are f32 by default, GT_ACARS_FORMAT=cs16 otherwise). A capture
// wider than 48 kHz is channelized by the production Downconverter;
// GT_ACARS_TUNE_HZ is the channel's offset from the capture centre.
//
// GT_ACARS_EXPECT=<n> asserts at least n CRC-valid blocks (default: report
// only). The CRC is the verdict, never "it printed something".
func TestACARSReplay(t *testing.T) {
	audioPath, iqPath := os.Getenv("GT_ACARS_AUDIO"), os.Getenv("GT_ACARS_IQ")
	if audioPath == "" && iqPath == "" {
		t.Skip("set GT_ACARS_AUDIO=<AM audio wav> or GT_ACARS_IQ=<capture> to replay an ACARS capture")
	}
	var runs []acarsRun
	if audioPath != "" {
		chans, rate := loadACARSAudio(t, audioPath)
		t.Logf("audio %s: %d channel(s) at %d Hz, %.1f s", audioPath, len(chans), rate, float64(len(chans[0]))/float64(rate))
		for i, ch := range chans {
			runs = append(runs, runACARSAudio(t, fmt.Sprintf("audio ch%d", i+1), ch, rate))
		}
	}
	if iqPath != "" {
		c := loadAFSKCapture(t, iqPath, "GT_ACARS_RATE", "GT_ACARS_FORMAT")
		if c.rate == 0 {
			t.Fatal("GT_ACARS_RATE is required for a headerless IQ capture")
		}
		t.Logf("iq %s: format=%s rate=%.0f Hz %.1f s %s", iqPath, c.format, c.rate, float64(c.length())/c.rate, c.signalStats())
		tune := 0.0
		if v := os.Getenv("GT_ACARS_TUNE_HZ"); v != "" {
			tune = fsEnvFloat(t, "GT_ACARS_TUNE_HZ", 0)
		}
		if c.audio != nil {
			runs = append(runs, runACARSAudio(t, "audio", c.audio, int(c.rate)))
		} else {
			runs = append(runs, runACARSIQ(t, c.iq, c.rate, tune))
		}
	}

	total := 0
	for _, r := range runs {
		t.Logf("%s: sync_locks=%d blocks=%d crc_ok=%d corrected=%d aborted=%d",
			r.name, r.stats.SyncLocks, r.stats.Blocks, r.stats.CRCOK, r.stats.Corrected, r.stats.Aborted)
		for _, m := range r.msgs {
			t.Logf("  t=%6.2fs crc_ok=%v fixed=%d %s raw=%s", m.t, m.msg.CRCOK, m.msg.Corrected, m.msg.Summary(), m.msg.RawHex)
		}
		total += int(r.stats.CRCOK)
	}
	want := 0
	if v := os.Getenv("GT_ACARS_EXPECT"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			t.Fatalf("GT_ACARS_EXPECT=%q: %v", v, err)
		}
		want = n
	}
	switch {
	case want == 0:
		t.Logf("VERDICT: report only (GT_ACARS_EXPECT unset): %d CRC-valid block(s)", total)
	case total >= want:
		t.Logf("VERDICT: PASS — %d CRC-valid block(s), wanted ≥ %d", total, want)
	default:
		t.Errorf("VERDICT: FAIL — %d CRC-valid block(s), wanted ≥ %d (sync_locks=0 ⇒ no MSK/rate match: check the rate and that the file is AM audio, not FM; locks but no valid blocks ⇒ signal too weak or a framing defect)", total, want)
	}
}

type acarsTimed struct {
	t   float64
	msg acars.Message
}

type acarsRun struct {
	name  string
	msgs  []acarsTimed
	stats acars.Stats
}

func runACARSAudio(t *testing.T, name string, audio []float32, rate int) acarsRun {
	t.Helper()
	res := acarsRun{name: name}
	var consumed int
	rcv, err := acarsrx.New(acarsrx.Options{InputRateHz: uint32(rate), OnMessage: func(m acars.Message) {
		res.msgs = append(res.msgs, acarsTimed{t: float64(consumed) / float64(rate), msg: m})
	}})
	if err != nil {
		t.Fatal(err)
	}
	const chunk = 4096
	for i := 0; i < len(audio); i += chunk {
		end := min(i+chunk, len(audio))
		rcv.ProcessAudio(audio[i:end])
		consumed = end
	}
	res.stats = rcv.Framer().Stats()
	return res
}

// acarsDDCTargetHz is the channel rate the scanner's data front end hands
// its decoders (~48 kHz).
const acarsDDCTargetHz = 48000

func runACARSIQ(t *testing.T, iq []complex64, rate, tuneHz float64) acarsRun {
	t.Helper()
	res := acarsRun{name: "iq"}
	var ddc *ccdecoder.Downconverter
	front := rate
	if rate > acarsDDCTargetHz || tuneHz != 0 {
		ddc = ccdecoder.NewDownconverterWithOffset(rate, acarsDDCTargetHz, tuneHz)
		front = ddc.OutRateHz()
	}
	var consumed int
	rcv, err := acarsrx.New(acarsrx.Options{InputRateHz: uint32(math.Round(front)), OnMessage: func(m acars.Message) {
		res.msgs = append(res.msgs, acarsTimed{t: float64(consumed) / rate, msg: m})
	}})
	if err != nil {
		t.Fatal(err)
	}
	const chunk = 4096
	var buf []complex64
	for i := 0; i < len(iq); i += chunk {
		end := min(i+chunk, len(iq))
		in := iq[i:end]
		if ddc != nil {
			buf = ddc.Process(buf, in)
			in = buf
		}
		rcv.ProcessIQ(in)
		consumed = end
	}
	res.stats = rcv.Framer().Stats()
	return res
}

// loadACARSAudio reads a WAV of AM-demodulated audio and returns one
// float32 slice per channel plus the sample rate. It accepts 8/16-bit PCM
// and 32-bit float, plain or WAVE_FORMAT_EXTENSIBLE, and skips any chunk
// before "data".
func loadACARSAudio(t *testing.T, path string) ([][]float32, int) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) < 12 || string(raw[0:4]) != "RIFF" || string(raw[8:12]) != "WAVE" {
		t.Fatalf("%s: not a RIFF/WAVE file", path)
	}
	var (
		format, channels, bitsPer uint16
		rate                      uint32
		data                      []byte
	)
	for p := 12; p+8 <= len(raw); {
		id, size := string(raw[p:p+4]), int(binary.LittleEndian.Uint32(raw[p+4:]))
		body := raw[p+8 : min(p+8+size, len(raw))]
		switch id {
		case "fmt ":
			format = binary.LittleEndian.Uint16(body[0:])
			channels = binary.LittleEndian.Uint16(body[2:])
			rate = binary.LittleEndian.Uint32(body[4:])
			bitsPer = binary.LittleEndian.Uint16(body[14:])
			if format == 0xFFFE && len(body) >= 26 { // WAVE_FORMAT_EXTENSIBLE
				format = binary.LittleEndian.Uint16(body[24:])
			}
		case "data":
			data = body
		}
		p += 8 + size + size&1
	}
	if channels == 0 || rate == 0 || data == nil {
		t.Fatalf("%s: missing fmt or data chunk", path)
	}
	frame := int(channels) * int(bitsPer) / 8
	n := len(data) / frame
	out := make([][]float32, channels)
	for c := range out {
		out[c] = make([]float32, n)
	}
	for i := 0; i < n; i++ {
		for c := 0; c < int(channels); c++ {
			off := i*frame + c*int(bitsPer)/8
			var v float32
			switch {
			case format == 3 && bitsPer == 32:
				v = math.Float32frombits(binary.LittleEndian.Uint32(data[off:]))
			case format == 1 && bitsPer == 16:
				v = float32(int16(binary.LittleEndian.Uint16(data[off:]))) / 32768
			case format == 1 && bitsPer == 8:
				v = (float32(data[off]) - 128) / 128
			default:
				t.Fatalf("%s: unsupported WAV encoding format=%d bits=%d", path, format, bitsPer)
			}
			out[c][i] = v
		}
	}
	return out, int(rate)
}
