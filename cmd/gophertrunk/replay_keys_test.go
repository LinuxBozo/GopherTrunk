package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"math"
	"math/cmplx"
	"math/rand"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/MattCheramie/GopherTrunk/internal/dsp/demod"
	"github.com/MattCheramie/GopherTrunk/internal/dsp/filter"
	"github.com/MattCheramie/GopherTrunk/internal/radio/dmr"
	dmrvoice "github.com/MattCheramie/GopherTrunk/internal/radio/dmr/voice"
	"github.com/MattCheramie/GopherTrunk/internal/radio/framing"
	"github.com/MattCheramie/GopherTrunk/internal/siglab"
	"github.com/MattCheramie/GopherTrunk/internal/trunking"
)

func TestParseReplayKey(t *testing.T) {
	for _, tc := range []struct {
		in      string
		ok      bool
		alg     string
		kid     uint16
		wantKey string
	}{
		{"rc4:11:4E77AD0B51", true, "rc4", 11, "4E77AD0B51"},
		{"adp:1:1234567890", true, "rc4", 1, "1234567890"},
		{"ARC4:0x16:B18852F4AE", true, "rc4", 22, "B18852F4AE"},
		{"6:0A0B0C0D0E", true, "rc4", 6, "0A0B0C0D0E"}, // KEYID:HEX defaults to RC4
		{"aes:1:00112233", false, "", 0, ""},           // not supported, as in config
		{"rc4:70000:0A0B", false, "", 0, ""},           // key ID out of range
		{"rc4:1:XYZ", false, "", 0, ""},                // not hex
		{"0A0B0C0D0E", false, "", 0, ""},               // no key ID
	} {
		e, err := parseReplayKey(tc.in)
		if (err == nil) != tc.ok {
			t.Errorf("%q: err=%v, want ok=%v", tc.in, err, tc.ok)
			continue
		}
		if tc.ok && (e.NormalizedAlgorithm() != tc.alg || e.KeyID != tc.kid || e.Key != tc.wantKey) {
			t.Errorf("%q: got %+v", tc.in, e)
		}
	}
	var f replayKeyFlags
	if err := f.Set("rc4:11:4E77AD0B51"); err != nil {
		t.Fatal(err)
	}
	if err := f.Set("rc4:11:B18852F4AE"); err == nil {
		t.Fatal("a repeated key ID was accepted")
	}
}

// epFixture is the committed real-air #1187 Enhanced Privacy capture: the
// reporter's PI header and first three voice superframes as received, with
// the key they published (internal/radio/dmr/voice/testdata/README.md).
type epFixture struct {
	KeyHex   string `json:"key_hex"`
	PIHeader struct {
		AlgID uint8  `json:"alg_id"`
		FID   uint8  `json:"fid"`
		KeyID uint8  `json:"key_id"`
		MI    string `json:"mi"`
		Dst   uint32 `json:"dst"`
	} `json:"pi_header"`
	Superframes []struct {
		EmbeddedIV string   `json:"embedded_iv"`
		OnAir      []string `json:"on_air"`
	} `json:"superframes"`
}

func loadEPFixture(t *testing.T) epFixture {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "internal", "radio", "dmr", "voice", "testdata", "ep_issue1187_ptt1.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fx epFixture
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatal(err)
	}
	return fx
}

func hexBits(t *testing.T, h string, n int) []byte {
	t.Helper()
	b, err := hex.DecodeString(h)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]byte, n)
	for k := range out {
		out[k] = (b[k>>3] >> uint(7-(k&7))) & 1
	}
	return out
}

func parseHex32(t *testing.T, s string) uint32 {
	t.Helper()
	v, err := strconv.ParseUint(s, 16, 32)
	if err != nil {
		t.Fatal(err)
	}
	return uint32(v)
}

// dataBurst frames 96 info bits as a BPTC(196,96) data burst of data type dt.
func dataBurst(info []byte, cc uint8, dt dmr.DataType) []uint8 {
	bits := make([]byte, 96)
	for i := range bits {
		bits[i] = (info[i>>3] >> uint(7-(i&7))) & 1
	}
	payload := framing.BitsToDibits(framing.EncodeBPTC196_96(bits))
	slot := framing.BitsToDibits(dmr.AssembleSlotType(dmr.SlotType{ColorCode: cc, DataType: dt}))
	b := make([]uint8, 0, dmr.BurstDibits)
	b = append(b, payload[:dmr.HalfPayloadDibits]...)
	b = append(b, slot[:dmr.SlotTypeDibits]...)
	b = append(b, dmr.BSData.Dibits[:]...)
	b = append(b, slot[dmr.SlotTypeDibits:]...)
	b = append(b, payload[dmr.HalfPayloadDibits:]...)
	return b
}

func bitsToDibits(bits []byte) []uint8 {
	d := make([]uint8, len(bits)/2)
	for i := range d {
		d[i] = bits[2*i]<<1 | bits[2*i+1]
	}
	return d
}

// voiceBurst lays three 72-bit on-air AMBE frames around a 24-dibit centre
// (the voice sync on burst A, an EMB + null embedded fragment on B–F).
func voiceBurst(frames [][]byte, centre []uint8) []uint8 {
	var bits []byte
	for _, f := range frames {
		bits = append(bits, f...)
	}
	b := make([]uint8, 0, dmr.BurstDibits)
	b = append(b, bitsToDibits(bits[:108])...)
	b = append(b, centre...)
	b = append(b, bitsToDibits(bits[108:])...)
	return b
}

// buildEPCallDibits is one slot of a DMR carrier (a burst every 288 dibits:
// CACH, our burst, the idle other slot) carrying an Enhanced Privacy call
// the way a radio keys it: Voice LC Headers (privacy flag set), PI headers,
// then the three REAL-AIR #1187 superframes, ciphertext and embedded IVs as
// received.
func buildEPCallDibits(t *testing.T, fx epFixture) []uint8 {
	t.Helper()
	const cc = 2 // the reporter's colour code
	// Lead-in, CACH and the other slot are RANDOM dibits: a repeating
	// pattern carries a fixed symbol mean no real carrier has, which the
	// receiver's AFC reads as a carrier offset (the CLAUDE.md fixture trap).
	rng := rand.New(rand.NewSource(1187))
	idle := func(n int) []uint8 {
		d := make([]uint8, n)
		for i := range d {
			d[i] = uint8(rng.Intn(4))
		}
		return d
	}
	var out []uint8
	slot := func(burst []uint8) {
		out = append(out, idle(12)...)
		out = append(out, burst...)
		out = append(out, idle(12+dmr.BurstDibits)...)
	}
	out = append(out, idle(1200)...)

	lc := dmr.AssembleFLC(dmr.FLC{FLCO: dmr.FLCOGroupVoiceUser, DstAddr: fx.PIHeader.Dst, SrcAddr: 3024109,
		ServiceOptions: 0x40}) // privacy
	var lcData [9]byte
	copy(lcData[:], lc)
	cw := framing.EncodeRS12_9(lcData)
	for i := 0; i < 3; i++ {
		cw[9+i] ^= framing.RS129SeedVoiceLCHeader[i]
	}
	for i := 0; i < 3; i++ {
		slot(dataBurst(cw[:], cc, dmr.DTVoiceLCHeader))
	}
	h := dmr.PIHeader{AlgID: fx.PIHeader.AlgID, FID: fx.PIHeader.FID, KeyID: fx.PIHeader.KeyID, DstAddr: fx.PIHeader.Dst}
	mi := parseHex32(t, fx.PIHeader.MI)
	h.MI = [4]byte{byte(mi >> 24), byte(mi >> 16), byte(mi >> 8), byte(mi)}
	for i := 0; i < 3; i++ {
		slot(dataBurst(dmr.AssemblePIHeader(h), cc, dmr.DTPIHeader))
	}
	// Bursts B–E carry the call's Full LC as four embedded fragments (what
	// binds the voice to its talkgroup), F a null fragment.
	lcBits := make([]byte, framing.EmbLCBits)
	for i := range lcBits {
		lcBits[i] = (lc[i>>3] >> uint(7-(i&7))) & 1
	}
	embLC := framing.EncodeEmbeddedLC(lcBits)
	lcss := []dmr.LCSS{dmr.LCSSFirst, dmr.LCSSCont, dmr.LCSSCont, dmr.LCSSLast}
	centreFor := func(b int) []uint8 {
		if b == 0 {
			return dmr.BSVoice.Dibits[:]
		}
		frag, ls := make([]byte, framing.EmbeddedFragmentBits), dmr.LCSSSingle
		if b <= 4 {
			frag = embLC[(b-1)*framing.EmbeddedFragmentBits : b*framing.EmbeddedFragmentBits]
			ls = lcss[b-1]
		}
		return framing.BitsToDibits(dmr.AssembleEmbeddedField(dmr.EMB{ColorCode: cc, PI: true, LCSS: ls}, frag))
	}
	for _, sf := range fx.Superframes {
		frames := make([][]byte, len(sf.OnAir))
		for i, f := range sf.OnAir {
			frames[i] = hexBits(t, f, dmrvoice.AMBEFrameBits)
		}
		for b := 0; b < dmrvoice.BurstsPerSuperframe; b++ {
			centre := centreFor(b)
			slot(voiceBurst(frames[b*dmrvoice.FramesPerBurst:(b+1)*dmrvoice.FramesPerBurst], centre))
		}
	}
	return append(out, idle(2400)...)
}

// expectedEP returns the fixture's 49-bit payloads as ciphertext and as the
// clear speech the published key recovers (MI chain: the PI header's for
// superframe 0, then each superframe's embedded IV names the next — the
// convention TestEPCaptureIssue1187 pins against the same capture).
func expectedEP(t *testing.T, fx epFixture) (cipher, clear [][]byte) {
	t.Helper()
	key, err := hex.DecodeString(fx.KeyHex)
	if err != nil {
		t.Fatal(err)
	}
	mi := parseHex32(t, fx.PIHeader.MI)
	for _, sf := range fx.Superframes {
		infos := make([][]byte, len(sf.OnAir))
		for i, f := range sf.OnAir {
			info, _, err := dmrvoice.DecodeAMBEFrame(hexBits(t, f, dmrvoice.AMBEFrameBits))
			if err != nil {
				t.Fatal(err)
			}
			infos[i] = info
			cipher = append(cipher, append([]byte(nil), info...))
		}
		if _, _, err := dmrvoice.DescrambleSuperframe(key, mi, infos); err != nil {
			t.Fatal(err)
		}
		clear = append(clear, infos...)
		mi = parseHex32(t, sf.EmbeddedIV)
	}
	return cipher, clear
}

// simplexKeyup is what a squelch-off receiver hears for one transmission:
// receiver noise, the keyed carrier (the dibits, C4FM-modulated at ±1944 Hz
// outer deviation, a weak noise floor under it, starting skew samples late so
// the call lands at an arbitrary symbol phase), then noise again after the
// unkey — channel-filtered to ±8 kHz the way a receiver filters ahead of its
// discriminator (without that, 96 kHz of FM noise dominates the "audio" and
// the disc sanity check rightly calls it no discriminator tap). The keyup
// after silence is what arms the DMR receiver's feed-forward timing
// acquisition; a stream that is carrier from its first sample has none, and
// the timing loop then crawls in from a bad phase on half the start phases
// (measured: phases 1-5 of every 10-sample symbol never locked in 2 s).
func simplexKeyup(dibits []uint8, rate float64, skew int) []complex64 {
	rng := rand.New(rand.NewSource(1187 + int64(skew)))
	noise := func(n int, sigma float64) []complex64 {
		x := make([]complex64, n)
		for i := range x {
			x[i] = complex64(complex(sigma*rng.NormFloat64(), sigma*rng.NormFloat64()))
		}
		return x
	}
	sig := demod.ModulateC4FM(dibits, int(rate/4800), 8, 0.20, rate, 1944)
	for i, n := range noise(len(sig), 0.03) {
		sig[i] += n
	}
	iq := noise(int(rate/2)+skew, 0.5)
	iq = append(iq, sig...)
	iq = append(iq, noise(int(rate/2), 0.5)...)
	return filter.NewFIR(filter.LowpassKaiser(129, 8000/rate, 8.6)).Process(nil, iq)
}

// writeDiscWAV FM-demodulates iq (phase difference per sample) into 16-bit
// mono discriminator audio at the given full-scale deviation — what a
// DSD-style receiver records.
func writeDiscWAV(t *testing.T, path string, iq []complex64, rate, dev float64) {
	t.Helper()
	pcm := make([]byte, 2*len(iq))
	prev := complex64(1)
	for i, x := range iq {
		f := cmplx.Phase(complex128(x)*cmplx.Conj(complex128(prev))) * rate / (2 * math.Pi)
		prev = x
		v := math.Max(-1, math.Min(1, f/dev))
		binary.LittleEndian.PutUint16(pcm[2*i:], uint16(int16(v*32767)))
	}
	var h bytes.Buffer
	h.WriteString("RIFF")
	binary.Write(&h, binary.LittleEndian, uint32(36+len(pcm)))
	h.WriteString("WAVEfmt ")
	for _, v := range []any{uint32(16), uint16(1), uint16(1), uint32(rate), uint32(2 * rate), uint16(2), uint16(16)} {
		binary.Write(&h, binary.LittleEndian, v)
	}
	h.WriteString("data")
	binary.Write(&h, binary.LittleEndian, uint32(len(pcm)))
	h.Write(pcm)
	if err := os.WriteFile(path, h.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

// recordedRawFrames reads every .raw sidecar the recorder wrote: DMR frames
// are 7-byte packed 49-bit AMBE+2 payloads, flat-concatenated.
func recordedRawFrames(t *testing.T, dir string) [][]byte {
	t.Helper()
	var out [][]byte
	_ = filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || filepath.Ext(p) != ".raw" {
			return nil
		}
		b, rerr := os.ReadFile(p)
		if rerr != nil {
			t.Fatal(rerr)
		}
		for i := 0; i+7 <= len(b); i += 7 {
			out = append(out, b[i:i+7])
		}
		return nil
	})
	return out
}

// matches counts recorded frames equal to one of want on bits 0..44 (bits
// 45..48 carry the embedded IV on air, as on a real radio).
func matches(got, want [][]byte) int {
	pack := func(bits []byte) []byte {
		p := make([]byte, 7)
		for i, b := range bits {
			p[i>>3] |= b << uint(7-(i&7))
		}
		return p
	}
	n := 0
	for _, g := range got {
		for _, w := range want {
			p := pack(w)
			if bytes.Equal(g[:5], p[:5]) && g[5]&0xF8 == p[5]&0xF8 {
				n++
				break
			}
		}
	}
	return n
}

// TestReplayDecryptsRealAirEnhancedPrivacyFromDiscAudio is the #1187 gate,
// run the way the reporter can: a DSD-style DISCRIMINATOR-AUDIO recording of
// an Enhanced Privacy call carrying the reporter's REAL ciphertext (the
// committed real-air superframes) goes through `replay -format disc` and the
// replay -record-voice voice path — siglab decode → engine → composer →
// recorder, the daemon's own chain — with `-key rc4:11:<their key>`. The
// recorder's .raw sidecar must hold the CLEAR speech payloads; without the
// key it holds the ciphertext (before -key existed that was the only
// outcome: replay built no KeyResolver, so an encrypted capture could not be
// checked offline through the daemon path at all).
func TestReplayDecryptsRealAirEnhancedPrivacyFromDiscAudio(t *testing.T) {
	fx := loadEPFixture(t)
	cipher, clear := expectedEP(t, fx)

	const rate = 96_000.0 // the reporter's recordings are 96 kHz
	wav := filepath.Join(t.TempDir(), "ep-call.wav")
	writeDiscWAV(t, wav, simplexKeyup(buildEPCallDibits(t, fx), rate, 0), rate, discDefaultDeviationHz)

	run := func(keyArgs ...string) [][]byte {
		var keys replayKeyFlags
		for _, k := range keyArgs {
			if err := keys.Set(k); err != nil {
				t.Fatal(err)
			}
		}
		iqPath, gotRate, sanity, cleanup, err := prepareDiscInput(wav, discDefaultDeviationHz)
		if err != nil {
			t.Fatal(err)
		}
		defer cleanup()
		if gotRate != rate {
			t.Fatalf("disc rate %v, want %v", gotRate, rate)
		}
		t.Logf("disc input: %s", sanity)
		var logw io.Writer = io.Discard
		if os.Getenv("GT_DEBUG_LOG") != "" {
			logw = os.Stderr
		}
		log := slog.New(slog.NewTextHandler(logw, &slog.HandlerOptions{Level: slog.LevelDebug}))
		outDir := t.TempDir()
		proto := trunking.ProtocolDMRTier2
		rig, err := setupReplayVoice(outDir, 48_000, 500*time.Millisecond, nil, replayKeyResolver(keys, log), log)
		if err != nil {
			t.Fatal(err)
		}
		cfg := siglab.Config{
			Protocol:   proto,
			SystemName: "replay",
			System: trunking.System{Name: "replay", Protocol: proto,
				DMRInterleavedVoice: trunking.DMRVoiceCadenceDetected(proto)},
			FrequencyHz:  446_500_000,
			SampleRateHz: gotRate,
			Format:       siglab.FormatF32,
			Log:          log,
			Bus:          rig.bus,
			OnChannelIQ:  rig.onChannelIQ,
		}
		res, err := siglab.Run(iqPath, cfg)
		if err != nil {
			t.Fatalf("siglab.Run: %v", err)
		}
		t.Logf("decode: locked=%v grants=%d", res.Locked, len(res.Grants))
		rig.finalize()
		return recordedRawFrames(t, outDir)
	}

	keyed := run("rc4:11:" + fx.KeyHex)
	if len(keyed) == 0 {
		t.Fatal("replay -record-voice recorded no voice frames from the call")
	}
	if c, k := matches(keyed, clear), matches(keyed, cipher); c < len(clear)-dmrvoice.FramesPerSuperframe || k != 0 {
		t.Fatalf("with -key: %d/%d recorded frames are the clear speech and %d are ciphertext (of %d recorded) — the key did not reach the voice path", c, len(clear), k, len(keyed))
	}
	t.Logf("with -key: %d recorded frames, %d/%d clear, %d ciphertext", len(keyed), matches(keyed, clear), len(clear), matches(keyed, cipher))
	plain := run()
	t.Logf("without -key: %d recorded frames, %d clear, %d/%d ciphertext", len(plain), matches(plain, clear), matches(plain, cipher), len(cipher))
	if c, k := matches(plain, clear), matches(plain, cipher); c != 0 || k < len(cipher)-dmrvoice.FramesPerSuperframe {
		t.Fatalf("without -key: %d clear / %d ciphertext frames of %d — a key-less replay must record the ciphertext untouched", c, k, len(plain))
	}
}

// The disc sanity line must pass a genuine squelch-off recording (noise
// between transmissions) and still flag the shape the #1187 reporter's first
// DMR files had: white-noise bursts to 15.5 kHz gated at the 30/30 ms slot
// cadence — re-recorded decoded audio, not a discriminator tap.
func TestDiscriminatorAudioSanity(t *testing.T) {
	const rate = 96_000.0
	fx := loadEPFixture(t)
	iq := simplexKeyup(buildEPCallDibits(t, fx), rate, 0)
	tap := make([]float32, len(iq))
	prev := complex64(1)
	for i, x := range iq {
		tap[i] = float32(cmplx.Phase(complex128(x)*cmplx.Conj(complex128(prev))) * rate / (2 * math.Pi) / discDefaultDeviationHz)
		prev = x
	}
	if got := discriminatorAudioSanity(tap, rate, "4FSK"); !bytes.Contains([]byte(got), []byte("consistent with a discriminator tap")) {
		t.Fatalf("genuine tap with noise gaps: %s", got)
	}
	rng := rand.New(rand.NewSource(7))
	lp := filter.NewFIR(filter.LowpassKaiser(129, 15500/rate, 8.6))
	white := make([]complex64, int(rate*3))
	for i := range white {
		white[i] = complex64(complex(rng.NormFloat64(), 0))
	}
	white = lp.Process(nil, white)
	garbled := make([]float32, len(white))
	for i, v := range white {
		if (i/int(rate*0.03))%2 == 0 { // 30 ms on, 30 ms off
			garbled[i] = 0.3 * real(v)
		}
	}
	if got := discriminatorAudioSanity(garbled, rate, "4FSK"); !bytes.Contains([]byte(got), []byte("NOT a demodulable")) {
		t.Fatalf("gated white-noise bursts (re-recorded decoded audio): %s", got)
	}
	if got := discriminatorAudioSanity(make([]float32, 50000), rate, "4FSK"); got != "audio is digital silence" {
		t.Fatalf("silence: %s", got)
	}
}

// A chain that subscribes after the decode has moved on still receives the
// call's opening (the pre-roll), and after a call start push holds until the
// chain subscribes instead of dropping chunks (#1187: the replay decode runs far
// faster than real time, and the opening carries the PI header).
func TestReplayVoiceSourcePrerollAndCallStartWait(t *testing.T) {
	src := newReplayVoiceSource(1000) // 1 s pre-roll = 1000 samples
	for i := 0; i < 15; i++ {
		src.push([]complex64{complex(float32(i), 0)}, 0)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, err := src.StreamIQ(ctx)
	if err != nil {
		t.Fatal(err)
	}
	first := <-ch
	if len(first) != 15 || real(first[0]) != 0 || real(first[14]) != 14 {
		t.Fatalf("pre-roll = %v, want the 15 samples pushed before subscription", first)
	}
	cancel()

	// After a call start with no subscriber, push waits for the chain.
	src2 := newReplayVoiceSource(1000)
	src2.expectSubscriber()
	pushed := make(chan struct{})
	go func() {
		src2.push([]complex64{42}, 0)
		close(pushed)
	}()
	select {
	case <-pushed:
		t.Fatal("push returned before any chain subscribed after a grant")
	case <-time.After(100 * time.Millisecond):
	}
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	ch2, err := src2.StreamIQ(ctx2)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-pushed:
	case <-time.After(time.Second / 2):
		t.Fatal("push still blocked after the chain subscribed")
	}
	got := <-ch2
	if len(got) == 0 || got[len(got)-1] != 42 {
		t.Fatalf("subscriber did not receive the held chunk: %v", got)
	}

	// A chain that subscribed BEFORE the start was reported (the bus listener
	// is asynchronous) leaves nothing to wait for once it is gone.
	src3 := newReplayVoiceSource(1000)
	ctx3, cancel3 := context.WithCancel(context.Background())
	if _, err := src3.StreamIQ(ctx3); err != nil {
		t.Fatal(err)
	}
	src3.expectSubscriber()
	cancel3()
	for deadline := time.Now().Add(time.Second); ; time.Sleep(time.Millisecond) {
		src3.mu.Lock()
		n := len(src3.subs)
		src3.mu.Unlock()
		if n == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("cancelled subscriber not removed")
		}
	}
	start := time.Now()
	src3.push([]complex64{1}, 0)
	if d := time.Since(start); d > 200*time.Millisecond {
		t.Fatalf("push stalled %v for a chain that had already subscribed", d)
	}
}
