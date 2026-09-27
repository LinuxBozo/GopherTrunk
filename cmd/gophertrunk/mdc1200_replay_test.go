package main

import (
	"encoding/binary"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/MattCheramie/GopherTrunk/internal/events"
	"github.com/MattCheramie/GopherTrunk/internal/radio/mdc1200"
	mdcafsk "github.com/MattCheramie/GopherTrunk/internal/radio/mdc1200/afsk"
	mdcrx "github.com/MattCheramie/GopherTrunk/internal/radio/mdc1200/receiver"
	"github.com/MattCheramie/GopherTrunk/internal/scanner/ccdecoder"
	"github.com/MattCheramie/GopherTrunk/internal/storage"
)

// TestMDC1200Replay is the on-air verification gate for the MDC1200
// decoder (#438 / #1220): it replays a real Motorola capture through the
// PRODUCTION front end (mdc1200/afsk: FM → FFSK → symbol timing →
// XOR-precoding decode → framer → decoder), prints a per-burst timeline,
// and asserts that the capture's known unit ID decodes CRC-valid. The
// decoder shipped for four months without ever having seen a real radio
// (its own tests encoded the same wrong line code it decoded), so this is
// the harness that closes that gap the moment a capture lands.
// Skip-guarded; run with:
//
//	GT_MDC1200_IQ=<capture> GT_MDC1200_RATE=<Hz> GT_MDC1200_UNIT=<hex> \
//	  go test ./cmd/gophertrunk -run 'TestMDC1200Replay$' -v
//
// Knobs (all optional):
//
//	GT_MDC1200_FORMAT   f32 (default; interleaved IEEE float32 I/Q), cs16,
//	                    or audio (mono float32 discriminator audio).
//	                    wav/flac containers are content-sniffed and carry
//	                    their own rate.
//	GT_MDC1200_RATE     input sample rate. If unset and the file is
//	                    headerless the harness PROBES common rates and
//	                    reports CRC-valid bursts per rate (a wrong rate
//	                    decodes nothing). GT_MDC1200_RATES overrides the
//	                    probe list (comma-separated).
//	GT_MDC1200_TUNE_HZ  channel offset inside a wideband capture. If unset
//	                    on a wideband capture, the strongest carriers are
//	                    searched and each tried.
//	GT_MDC1200_UNIT     the transmitting radio's unit ID in hex (as the
//	                    radio's CPS shows it, e.g. 1234). Unset or 0
//	                    disables the assertion and only reports.
func TestMDC1200Replay(t *testing.T) {
	path := os.Getenv("GT_MDC1200_IQ")
	if path == "" {
		t.Skip("set GT_MDC1200_IQ (+ GT_MDC1200_RATE, GT_MDC1200_UNIT) to replay an MDC1200 capture")
	}
	capt := loadAFSKCapture(t, path, "GT_MDC1200_RATE", "GT_MDC1200_FORMAT")
	t.Logf("capture: %s format=%s samples=%d rate=%s", path, capt.format, capt.length(), rateOrProbe(capt.rate))
	t.Logf("signal: %s", capt.signalStats())

	tuneHz := fsEnvFloat(t, "GT_MDC1200_TUNE_HZ", 0)
	wantUnit := mdcEnvUnit(t)

	if capt.rate == 0 {
		capt.rate = probeMDC1200Rate(t, capt, tuneHz)
		if capt.rate == 0 {
			t.Fatalf("no candidate rate produced a CRC-valid burst; set GT_MDC1200_RATE to the capture's true rate (and GT_MDC1200_FORMAT if it is not float32 I/Q)")
		}
	}
	if os.Getenv("GT_MDC1200_TUNE_HZ") == "" && capt.audio == nil && capt.rate > fleetSyncDDCTargetHz {
		tuneHz = searchMDC1200Carrier(t, capt)
	}

	res := runMDC1200Chain(t, capt, tuneHz)
	t.Logf("=== sync_locks=%d framed=%d crc_ok=%d crc_fail=%d bits=%d",
		res.inner.BurstsIn, res.inner.BurstsEmitted, res.crcOK, res.inner.BurstsBadCRC, res.front.BitsEmitted)
	hits := 0
	for _, m := range res.msgs {
		flag := "  "
		if m.msg.CRCOK {
			flag = "OK"
		}
		t.Logf("  t=%8.3fs %s unit=%04X op=%02X arg=%02X %-20s raw=%s", m.t, flag, m.msg.UnitID, m.msg.Op, m.msg.Arg, m.msg.Operation, m.msg.RawHex)
		if m.msg.CRCOK && (wantUnit == 0 || m.msg.UnitID == wantUnit) {
			hits++
		}
	}

	switch {
	case wantUnit == 0:
		t.Logf("VERDICT: report only (GT_MDC1200_UNIT unset); CRC-valid bursts=%d", hits)
	case hits > 0:
		t.Logf("VERDICT: PASS — unit %04X decodes CRC-valid %d time(s) through the production front end", wantUnit, hits)
	default:
		t.Errorf("VERDICT: FAIL — unit %04X never decoded CRC-valid (check rate/format/tune first: a wrong rate decodes nothing; sync locks with CRC failures mean the framing or field layout is the next suspect; zero sync locks on a clean signal means the line code or tones)", wantUnit)
	}
}

// mdcEnvUnit parses GT_MDC1200_UNIT as hex (the way CPS and the panel
// show unit IDs). 0 / unset ⇒ report only.
func mdcEnvUnit(t *testing.T) uint16 {
	t.Helper()
	v := strings.TrimPrefix(strings.ToLower(os.Getenv("GT_MDC1200_UNIT")), "0x")
	if v == "" {
		return 0
	}
	u, err := strconv.ParseUint(v, 16, 16)
	if err != nil {
		t.Fatalf("bad GT_MDC1200_UNIT=%q: want a hex unit ID such as 1234", v)
	}
	return uint16(u)
}

type mdcTimed struct {
	t   float64
	msg storage.MDC1200Message
}

type mdcRun struct {
	msgs  []mdcTimed
	crcOK int
	inner mdcrx.Stats
	front mdcafsk.Stats
}

// runMDC1200Chain replays the capture through the production front end. A
// wideband (or offset) IQ capture goes through the production
// Downconverter to a 48 kHz channel slice first; a narrowband one feeds
// the front end directly; discriminator audio skips the FM demod.
func runMDC1200Chain(t *testing.T, c *fleetSyncCapture, tuneHz float64) mdcRun {
	t.Helper()
	var res mdcRun
	bus := events.NewBus(1024)
	sub := bus.Subscribe()
	var consumed uint64
	// The receiver publishes synchronously from ProcessIQ/ProcessAudio, so
	// a non-blocking drain after each chunk sees every burst of that chunk
	// (Bus.Publish drops when a subscriber's buffer is full; 1024 is far
	// beyond what one 4096-sample chunk can frame).
	collect := func() {
		for {
			select {
			case ev, ok := <-sub.C:
				if !ok {
					return
				}
				if ev.Kind != events.KindMDC1200Message {
					continue
				}
				if m, ok := ev.Payload.(storage.MDC1200Message); ok {
					res.msgs = append(res.msgs, mdcTimed{t: float64(consumed) / c.rate, msg: m})
					if m.CRCOK {
						res.crcOK++
					}
				}
			default:
				return
			}
		}
	}

	const chunk = 4096
	if c.audio != nil {
		rcv, err := mdcafsk.New(mdcafsk.Options{InputRateHz: uint32(math.Round(c.rate)), Bus: bus})
		if err != nil {
			t.Fatal(err)
		}
		for i := 0; i < len(c.audio); i += chunk {
			end := min(i+chunk, len(c.audio))
			rcv.ProcessAudio(c.audio[i:end])
			consumed = uint64(end)
			collect()
		}
		res.inner, res.front = rcv.Inner().Stats(), rcv.Stats()
		sub.Close()
		bus.Close()
		return res
	}

	var ddc *ccdecoder.Downconverter
	frontRate := c.rate
	if c.rate > fleetSyncDDCTargetHz || tuneHz != 0 {
		ddc = ccdecoder.NewDownconverterWithOffset(c.rate, fleetSyncDDCTargetHz, tuneHz)
		frontRate = ddc.OutRateHz()
	}
	rcv, err := mdcafsk.New(mdcafsk.Options{InputRateHz: uint32(math.Round(frontRate)), Bus: bus})
	if err != nil {
		t.Fatal(err)
	}
	var ddcBuf []complex64
	for i := 0; i < len(c.iq); i += chunk {
		end := min(i+chunk, len(c.iq))
		in := c.iq[i:end]
		if ddc != nil {
			ddcBuf = ddc.Process(ddcBuf, in)
			in = ddcBuf
		}
		rcv.ProcessIQ(in)
		consumed = uint64(end)
		collect()
	}
	res.inner, res.front = rcv.Inner().Stats(), rcv.Stats()
	sub.Close()
	bus.Close()
	return res
}

// searchMDC1200Carrier ranks the carriers in a wideband capture (the
// FleetSync harness's averaged-spectrum search) and returns the first
// that yields a CRC-valid burst, 0 when none does.
func searchMDC1200Carrier(t *testing.T, c *fleetSyncCapture) float64 {
	t.Helper()
	cands := fleetSyncCarrierCandidates(c.iq, c.rate, 8)
	if len(cands) == 0 {
		t.Logf("carrier search: no carrier stands 6 dB above the floor; running at centre")
		return 0
	}
	for i, cand := range cands {
		res := runMDC1200Chain(t, c, cand.offsetHz)
		t.Logf("carrier search: candidate %d at %+.0f Hz (%.1f dB over the floor): sync_locks=%d crc_ok=%d",
			i+1, cand.offsetHz, cand.aboveDb, res.inner.BurstsIn, res.crcOK)
		if res.crcOK > 0 {
			t.Logf("carrier search: tuning to %+.0f Hz (set GT_MDC1200_TUNE_HZ to override)", cand.offsetHz)
			return cand.offsetHz
		}
	}
	t.Logf("carrier search: no candidate decoded a CRC-valid burst; running at centre")
	return 0
}

// probeMDC1200Rate sweeps candidate sample rates for a headerless capture
// of unknown rate and returns the one with the most CRC-valid bursts.
func probeMDC1200Rate(t *testing.T, c *fleetSyncCapture, tuneHz float64) float64 {
	t.Helper()
	candidates := []float64{8000, 9600, 11025, 12000, 16000, 22050, 24000, 25000, 32000, 44100, 48000, 50000, 96000, 100000, 192000, 200000, 250000, 256000, 500000, 1000000, 1024000, 2000000, 2048000, 2400000, 2500000, 3000000}
	if v := os.Getenv("GT_MDC1200_RATES"); v != "" {
		candidates = nil
		for _, s := range strings.Split(v, ",") {
			f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
			if err != nil {
				t.Fatalf("bad GT_MDC1200_RATES entry %q", s)
			}
			candidates = append(candidates, f)
		}
	}
	type row struct {
		rate  float64
		crcOK int
		locks uint64
	}
	var rows []row
	for _, rate := range candidates {
		probe := *c
		probe.rate = rate
		res := runMDC1200Chain(t, &probe, tuneHz)
		rows = append(rows, row{rate, res.crcOK, res.inner.BurstsIn})
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].crcOK > rows[j].crcOK })
	t.Logf("rate probe (GT_MDC1200_RATE unset): rate → CRC-valid bursts (sync locks)")
	for _, r := range rows {
		if r.crcOK > 0 || r.locks > 0 {
			t.Logf("  %9.0f Hz: crc_ok=%d locks=%d", r.rate, r.crcOK, r.locks)
		}
	}
	if len(rows) == 0 || rows[0].crcOK == 0 {
		return 0
	}
	t.Logf("  → using %.0f Hz", rows[0].rate)
	return rows[0].rate
}

// ---- harness self-check ----------------------------------------------

// synthMDC1200IQ renders two keyups — a PTT ID from unit 0x1234 and a radio
// check from unit 0xABCD, each precoded from the encoder's start state as
// a radio does per transmission — with quiet carrier between, FFSK-
// modulated onto an FM carrier shifted by offsetHz.
func synthMDC1200IQ(t *testing.T, rate, offsetHz float64) []complex64 {
	t.Helper()
	quiet := func(sec float64) []complex64 {
		q := make([]complex64, int(48000*sec))
		for i := range q {
			q[i] = 1
		}
		return q
	}
	var iq []complex64
	iq = append(iq, quiet(0.5)...)
	iq = append(iq, demodModulateFFSK(mdc1200.Precode(mdc1200.BurstBits(0x01, 0x80, 0x1234)), 48000)...)
	iq = append(iq, quiet(0.3)...)
	iq = append(iq, demodModulateFFSK(mdc1200.Precode(mdc1200.BurstBits(0x63, 0x00, 0xABCD)), 48000)...)
	iq = append(iq, quiet(0.5)...)
	if rate != 48000 {
		iq = interpolateIQ(iq, 48000, rate)
	}
	if offsetHz != 0 {
		step := 2 * math.Pi * offsetHz / rate
		for i, s := range iq {
			c, sn := math.Cos(step*float64(i)), math.Sin(step*float64(i))
			re, im := float64(real(s)), float64(imag(s))
			iq[i] = complex(float32(re*c-im*sn), float32(re*sn+im*c))
		}
	}
	return iq
}

func assertMDC1200Run(t *testing.T, res mdcRun) {
	t.Helper()
	var ptt, check int
	for _, m := range res.msgs {
		switch {
		case m.msg.CRCOK && m.msg.Op == 0x01 && m.msg.Arg == 0x80 && m.msg.UnitID == 0x1234:
			ptt++
		case m.msg.CRCOK && m.msg.Op == 0x63 && m.msg.Arg == 0x00 && m.msg.UnitID == 0xABCD:
			check++
		default:
			t.Errorf("unexpected burst %+v", m.msg)
		}
	}
	if ptt != 1 || check != 1 {
		t.Fatalf("decoded ptt=%d check=%d CRC-valid bursts, want 1 and 1 (framer stats %+v)", ptt, check, res.inner)
	}
	if res.msgs[0].t <= 0.4 || res.msgs[0].t > 2.0 {
		t.Errorf("first burst timestamp %.3fs, want inside the modulated span after the 0.5 s lead-in", res.msgs[0].t)
	}
}

// TestMDC1200ReplayHarnessSelfCheck pins the harness itself, so a FAIL on
// a real capture means the capture or the decoder, never a broken harness.
func TestMDC1200ReplayHarnessSelfCheck(t *testing.T) {
	dir := t.TempDir()

	t.Run("narrowband f32 + rate probe", func(t *testing.T) {
		iq := synthMDC1200IQ(t, 48000, 0)
		buf := make([]byte, 8*len(iq))
		for i, s := range iq {
			binary.LittleEndian.PutUint32(buf[i*8:], math.Float32bits(real(s)))
			binary.LittleEndian.PutUint32(buf[i*8+4:], math.Float32bits(imag(s)))
		}
		path := dir + "/synthetic_mdc1200_f32.iq"
		if err := os.WriteFile(path, buf, 0o644); err != nil {
			t.Fatal(err)
		}
		t.Setenv("GT_MDC1200_FORMAT", "f32")
		t.Setenv("GT_MDC1200_RATE", "")
		t.Setenv("GT_MDC1200_RATES", "24000,48000,96000")
		c := loadAFSKCapture(t, path, "GT_MDC1200_RATE", "GT_MDC1200_FORMAT")
		if c.rate != 0 || len(c.iq) == 0 {
			t.Fatalf("loaded rate=%v samples=%d, want unknown rate and samples", c.rate, len(c.iq))
		}
		if got := probeMDC1200Rate(t, c, 0); got != 48000 {
			t.Fatalf("rate probe picked %.0f, want 48000", got)
		}
		c.rate = 48000
		assertMDC1200Run(t, runMDC1200Chain(t, c, 0))
	})

	t.Run("wideband cs16, carrier found by search", func(t *testing.T) {
		const rate, off = 250000.0, 62500.0
		c := &fleetSyncCapture{format: "cs16", rate: rate, iq: synthMDC1200IQ(t, rate, off)}
		got := searchMDC1200Carrier(t, c)
		if math.Abs(got-off) > 500 {
			t.Fatalf("carrier search found %+.0f Hz, want %+.0f", got, off)
		}
		assertMDC1200Run(t, runMDC1200Chain(t, c, got))
	})

	t.Run("discriminator audio", func(t *testing.T) {
		audio := demodFM(synthMDC1200IQ(t, 48000, 0))
		buf := make([]byte, 4*len(audio))
		for i, s := range audio {
			binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(s))
		}
		path := dir + "/synthetic_mdc1200_audio.f32"
		if err := os.WriteFile(path, buf, 0o644); err != nil {
			t.Fatal(err)
		}
		t.Setenv("GT_MDC1200_FORMAT", "audio")
		t.Setenv("GT_MDC1200_RATE", "48000")
		c := loadAFSKCapture(t, path, "GT_MDC1200_RATE", "GT_MDC1200_FORMAT")
		if c.audio == nil {
			t.Fatal("audio format did not load as audio")
		}
		assertMDC1200Run(t, runMDC1200Chain(t, c, 0))
	})

	t.Run("unit env parses hex", func(t *testing.T) {
		t.Setenv("GT_MDC1200_UNIT", "0xAbCd")
		if got := mdcEnvUnit(t); got != 0xABCD {
			t.Fatalf("mdcEnvUnit = %04X, want ABCD", got)
		}
		t.Setenv("GT_MDC1200_UNIT", "")
		if got := mdcEnvUnit(t); got != 0 {
			t.Fatalf("mdcEnvUnit unset = %04X, want 0", got)
		}
	})
}
