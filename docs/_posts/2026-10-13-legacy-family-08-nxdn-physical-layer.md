---
title: "The Legacy Family End to End, Part 8: NXDN Physical Layer — 4FSK at 4800, FSW, LICH and Scrambling"
description: "NXDN's air interface as GopherTrunk receives it: the 4800-symbol-per-second 4FSK receiver with its deviation-calibrated slicer and symbol AGC, the direction-specific Frame Sync Words 0xC55A and 0x3AA5, the doubled-bit LICH, the 192-dibit frame, the 15-bit scrambler model, the diagnostic taps behind the nxdn symbol scope — and why the two committed sample WAVs do not decode."
category: deep-dives
keywords: nxdn decoder sdr, nxdn 4fsk 4800 baud, nxdn frame sync word 0xc55a, nxdn lich decode, nxdn_deviation_hz, nxdn scrambler lfsr, nxdn96 vs nxdn48, nxdn symbol scope, c4fm symbol agc, gophertrunk legacy family end to end
tags: [legacy-family-end-to-end, nxdn, 4fsk, receiver, dsp, go]
author: Matt Cheramie
image: /assets/gophertrunk-logo.png
series: "The Legacy Family End to End"
series_part: 8
---

*Part 8 of **The Legacy Family End to End**, a 14-part deep dive through
the protocols the P25, DMR and TETRA series left out, each placed honestly
on one verification ladder.
[Part 7]({{ '/blog/deep-dives/legacy-family-07-analog-voice-on-trunked-fm/' | relative_url }})
closed the FM-era half of the family at the composer's analog chain. This
part opens the AMBE-era half with NXDN's physical layer — the one legacy
receiver that carries the full C4FM-family instrument set (symbol AGC, soft
and eye taps, an opt-in AFC, a symbol-scope panel) and has still never seen
a frame of real air it could decode.*

> **TL;DR:** `internal/radio/nxdn/receiver` decodes the 9600 bps NXDN
> variant: 4-level FSK at `SymbolRate` 4800 with α = 0.20 RRC, through
> `demod.FM` → `demod.C4FM` matched filter → `sync.MuellerMuller` (10 sps
> at the 48 kHz `ddcTargetRateHz`) → optional `demod.CoarseAFC`
> (`nxdn_afc`) → `demod.C4FMSymbolAGC` → 4-level slicer → `SymbolToDibit`
> (+3→01, +1→00, −1→10, −3→11). The slicer is calibrated to
> `DeviationHz` 1800 (`nxdn_deviation_hz` overrides it), and the AGC exists
> because the unit-energy RRC has ~3.1× DC gain on a rectangular stream.
> `nxdn.SyncDetector` matches the 8-dibit FSW — `0xC55A` outbound,
> `0x3AA5` inbound — within one dibit; `ControlChannel.Process` honours only
> outbound hits. A 192-dibit, 80 ms frame is FSW 8 + LICH 8 + SACCH 32 +
> Info 144; the LICH's 8 information bits are sent twice and majority-voted.
> `nxdn.Scrambler` is a 15-bit Fibonacci LFSR (x¹⁵ + x¹⁴ + 1) explicitly
> labelled a synthetic model. Rung: **synthetic-verified only** — the
> committed `NXDN48 IQ.wav` finds no FSW (likely BFSK) and `NXDN96 IQ.wav`
> slices bimodal (3 / 50 / 3 / 44 %), the deviation signature; the real-air
> harness is waiting.

**Key takeaways**

- **NXDN is the P25 Phase 1 modulation with different framing.** Same
  4800 sym/s 4FSK, same α = 0.20, same 1800 Hz deviation, same Gray
  mapping; the receiver's matched filter parameters carry over unchanged.
- **The slicer is only as right as its deviation.** `slicerScale =
  2π·DeviationHz/SampleRateHz`; a transmitter at 2400 Hz instead of 1800
  pushes the inner ±1 symbols over the outer threshold, and the committed
  NXDN96 sample shows exactly that.
- **The AGC is a correction for the matched filter, not for the air.**
  Unit-energy RRC overshoots a rectangular symbol stream ~3.1×; without the
  AGC, inner symbols collapse onto the outer rails and the sync still
  matches — "locks then decodes nothing".
- **Everything here is self-generated.** Every fixture is `ModulateC4FM`
  or a phase ramp; the two real WAVs do not decode, and
  `docs/decoder-capture-needs.md` files NXDN in Tier 1.

## Cheat sheet

| Concern | What it does | Where it lives |
|---|---|---|
| Receiver | FM → RRC(α 0.20, span 8) → MM(10 sps, gain 0.05) → [AFC] → AGC → slice → Gray dibits | `internal/radio/nxdn/receiver/receiver.go` |
| Slicer calibration | `slicerScale = 2π·DeviationHz/Fs`; default 1800 Hz; `nxdn_deviation_hz` overrides | `receiver.go` (`Options.DeviationHz`), `ccdecoder/pipelines.go` (`newNXDNPipeline`) |
| Symbol AGC | `C4FMSymbolAGC{Target: C4FMAGCTarget(scale, dev), Rate: 1/256}` | `receiver.go`, `receiver/agc_test.go` |
| Frame sync | `FSWOutboundHex` 0xC55A / `FSWInboundHex` 0x3AA5, 8 dibits, tolerance 1 | `internal/radio/nxdn/sync.go` (`SyncDetector`, `Match.Inbound`) |
| Frame | 192 dibits / 80 ms: FSW 8 · LICH 8 · SACCH 32 · Info 144 | `frame.go` (`FrameDibits`, `OffsetInfo`) |
| LICH | 8 info bits × 2 on the wire, majority vote, even parity | `lich.go` (`EncodeLICHWire`, `DecodeLICHWire`, `ParseLICH`) |
| Scrambler | 15-bit LFSR x¹⁵ + x¹⁴ + 1, key 0..32767, synthetic model | `scramble.go` (`Scrambler`, `ScramblerKeyMax`) |
| Scope | `nxdn` receiver at `nxdnDeviationHz` 1800, no AFC, AGC/clock readouts | `internal/scanner/symbolscope/scope.go` |

## In this post

- **Two channel rates, one receiver** — NXDN48, NXDN96, and which one the code decodes.
- **From IQ to dibits** — the chain, the deviation-calibrated slicer and the AGC that made real captures sliceable.
- **Frame sync and the 80 ms frame** — direction-specific FSWs and the 192-dibit layout.
- **The LICH** — eight bits sent twice, and what the RF-channel-type bit routes.
- **The scrambler model** — a 15-bit LFSR the code refuses to call confirmed.
- **Taps, scope and the two WAVs** — what the instruments show, and the rung.

## Two channel rates, one receiver

NXDN ([reference]({{ '/reference/nxdn/' | relative_url }})) runs at one of
two channel rates, and `frame.go` names both: `Rate4800` — BFSK, one bit
per symbol, the 6.25 kHz "NXDN48" — and `Rate9600` — 4-FSK, one dibit per
symbol at 4800 symbols per second, the 12.5 kHz "NXDN96". The logical frame
is identical at both; only the symbol mapping differs. GopherTrunk's
receiver decodes **one** of them. `receiver.go`'s package comment says it
in its first sentence: the 9600-baud 4-FSK variant, "the most common
deployment; the 4800-baud BFSK variant uses a 2-level slicer and lives in a
follow-up", and `docs/protocol-feature-parity.md` lists that follow-up in
its backlog as "a second demod variant against a frame layout still
unverified on air". `newNXDNPipeline` constructs the control channel with
`nxdn.Rate9600`, full stop.

[Protocol Decoders Part 6]({{ '/blog/deep-dives/protocol-decoders-06-nxdn-dpmr/' | relative_url }})
drew the frame and the CAC chain at the structural level; this part stays
below the frame — the symbols, the sync, the two small fields a decoder
reads before it knows whether it is on a control channel — and
[Part 9]({{ '/blog/deep-dives/legacy-family-09-nxdn-cac-and-trunking/' | relative_url }})
takes the CAC coding and the trunking state machine.

## From IQ to dibits

The chain is the C4FM family's
([C4FM]({{ '/reference/c4fm/' | relative_url }})), and the receiver's
header says why nothing had to change: "the same modulation P25 Phase 1,
DMR, and YSF use, so the matched-filter parameters carry over unchanged."

```go
// internal/radio/nxdn/receiver/receiver.go (shape)
const (
    SymbolRate       = 4800.0
    RolloffAlpha     = 0.20
    PulseSpanSymbols = 8
)
slicerScale := 1.0
if opts.DeviationHz > 0 {
    slicerScale = 2.0 * math.Pi * opts.DeviationHz / opts.SampleRateHz
}
r.disc    = r.fm.Process(r.disc, iq)                 // demod.FM
r.matched = r.mf.MatchedFilter(r.matched, r.disc)    // demod.C4FM, RRC
r.symbols = r.clock.Process(r.symbols, r.matched)    // sync.MuellerMuller(sps, 0.05)
if r.afc != nil { r.afc.Process(r.symbols) }         // nxdn_afc only
agcLevel := r.agc.Process(r.symbols)                 // demod.C4FMSymbolAGC
r.sliced  = r.mf.SliceMany(r.sliced, r.symbols)      // {-3,-1,+1,+3}
for i, sym := range r.sliced { r.dibits[i] = SymbolToDibit(sym) }
```

Three things are NXDN-specific in that listing. The first is the
**deviation calibration**. The FM discriminator's output is a phase
increment per sample, so a symbol at ±3 sits at `2π·1800/48000` rad per
sample, and `demod.NewC4FM(sps, span, alpha, slicerScale)` places its
thresholds at the physical level instead of the legacy ±1. `newNXDNPipeline`
passes 1800 Hz — the Common Air Interface value, the same number P25 Phase 1
uses — unless `nxdn_deviation_hz` is set, and the receiver's `DeviationHz
<= 0` path keeps `slicerScale` 1.0 so pre-scaled fixtures stay
byte-identical (`TestReceiverLegacyFixturePathUnchanged`).

The second is the **symbol AGC**, and its test comment is the clearest
statement of why the field failure looked the way it did. The RRC matched
filter is normalised to unit *energy*, which gives it a DC gain of about
3.1; a real transmitter's symbols are rectangular, not RRC-shaped, so the
matched filter's symbol centres land ~3.1× above the slicer's fixed
thresholds, the inner ±1 symbols cross the outer `2·deviation/3` boundary
and slice as ±3, and every payload comes back uncorrectable while the
coarser FSW still matches — "the control channel locks then decodes
nothing". `C4FMSymbolAGC` renormalises the running mean |x| to the level a
balanced 4-level stream should sit at (`C4FMAGCTarget(slicerScale,
DeviationHz)`, rate 1/256), the same calibration the P25 Phase 1 and DMR
receivers run from issue #275. `TestReceiverDecodesOverScaledC4FM` pins it
with `makeRunC4FMIQ`, a rectangular stream holding each symbol for a run so
the eye is clean of ISI: without the AGC the match rate falls to ~50 % as
the two inner dibits collapse onto the outer ones.

The third is the **optional AFC**, built only when `EnableAFC &&
DeviationHz > 0`: `demod.NewCoarseAFC(1)` applied post-clock on the symbol
stream, the DMR #836 design
([DMR End to End Part 9]({{ '/blog/deep-dives/dmr-end-to-end-09-direct-mode-carrier-gate/' | relative_url }})).
It is off by default for a reason that belongs to the CAC and so waits for
Part 9. Here it is enough to note where it sits — before the AGC, so the
level normalisation and the soft-LLR derivation see a centred eye — and
that `TestReceiverIsCarrierOffsetInvariant` pins the flag-on path: a 400 Hz
offset must leave ≥ 0.95 of dibits agreeing with the zero-offset decode.

`SymbolToDibit` is the C4FM family's Gray mapping — +3 → 01, +1 → 00,
−1 → 10, −3 → 11 — and `TestSymbolToDibitMatchesP25Phase1Convention` pins
it so "a future spec re-read doesn't silently desync from the FSW patterns
in the parent nxdn package". That sentence is the self-consistent trap
named in advance: the FSW constants and the mapping were written together,
and a capture is what confirms both.

<figure class="lab-figure">
<svg viewBox="0 0 680 190" width="680" height="190" role="img" aria-label="The NXDN receiver chain as a row of stages: IQ, FM discriminator, RRC matched filter at alpha 0.20 and span 8, Mueller-Muller timing at 10 samples per symbol, an optional dashed CoarseAFC stage labelled nxdn_afc opt-in, the symbol AGC with target from C4FMAGCTarget and rate one over 256, the four-level slicer with thresholds at plus and minus two thirds of the slicer scale, and SymbolToDibit. Below the slicer a small eye diagram shows four levels at plus three, plus one, minus one and minus three, with the slicer thresholds between them. An annotation under the matched filter reads DC gain about 3.1 on rectangular symbols, which the AGC removes.">
  <rect x="8" y="30" width="40" height="30" fill="none" stroke="currentColor"/>
  <text x="28" y="49" text-anchor="middle" fill="currentColor" font-size="8">IQ</text>
  <line x1="48" y1="45" x2="60" y2="45" stroke="currentColor"/>
  <rect x="60" y="30" width="60" height="30" fill="none" stroke="currentColor"/>
  <text x="90" y="49" text-anchor="middle" fill="currentColor" font-size="8">demod.FM</text>
  <line x1="120" y1="45" x2="132" y2="45" stroke="currentColor"/>
  <rect x="132" y="30" width="90" height="30" fill="none" stroke="currentColor"/>
  <text x="177" y="44" text-anchor="middle" fill="currentColor" font-size="8">RRC matched</text>
  <text x="177" y="55" text-anchor="middle" fill="var(--fg-muted)" font-size="7">α 0.20 · span 8</text>
  <line x1="222" y1="45" x2="234" y2="45" stroke="currentColor"/>
  <rect x="234" y="30" width="80" height="30" fill="none" stroke="currentColor"/>
  <text x="274" y="44" text-anchor="middle" fill="currentColor" font-size="8">Mueller-Müller</text>
  <text x="274" y="55" text-anchor="middle" fill="var(--fg-muted)" font-size="7">10 sps · gain 0.05</text>
  <line x1="314" y1="45" x2="326" y2="45" stroke="currentColor"/>
  <rect x="326" y="30" width="70" height="30" fill="none" stroke="var(--fg-muted)" stroke-dasharray="4 3"/>
  <text x="361" y="44" text-anchor="middle" fill="var(--fg-muted)" font-size="8">CoarseAFC</text>
  <text x="361" y="55" text-anchor="middle" fill="var(--fg-muted)" font-size="7">nxdn_afc · off</text>
  <line x1="396" y1="45" x2="408" y2="45" stroke="currentColor"/>
  <rect x="408" y="30" width="76" height="30" fill="none" stroke="var(--accent)"/>
  <text x="446" y="44" text-anchor="middle" fill="var(--accent)" font-size="8">symbol AGC</text>
  <text x="446" y="55" text-anchor="middle" fill="var(--fg-muted)" font-size="7">rate 1/256</text>
  <line x1="484" y1="45" x2="496" y2="45" stroke="currentColor"/>
  <rect x="496" y="30" width="76" height="30" fill="none" stroke="var(--accent)"/>
  <text x="534" y="44" text-anchor="middle" fill="var(--accent)" font-size="8">4-level slicer</text>
  <text x="534" y="55" text-anchor="middle" fill="var(--fg-muted)" font-size="7">±2·scale/3</text>
  <line x1="572" y1="45" x2="584" y2="45" stroke="currentColor"/>
  <rect x="584" y="30" width="88" height="30" fill="none" stroke="currentColor"/>
  <text x="628" y="44" text-anchor="middle" fill="currentColor" font-size="8">SymbolToDibit</text>
  <text x="628" y="55" text-anchor="middle" fill="var(--fg-muted)" font-size="7">+3→01 +1→00 −1→10 −3→11</text>
  <text x="177" y="76" text-anchor="middle" fill="var(--fg-muted)" font-size="7">unit energy ⇒ ~3.1× DC gain on rectangular symbols</text>
  <text x="446" y="76" text-anchor="middle" fill="var(--fg-muted)" font-size="7">renormalises mean|x| to C4FMAGCTarget</text>
  <line x1="420" y1="100" x2="640" y2="100" stroke="var(--fg-muted)"/>
  <line x1="420" y1="122" x2="640" y2="122" stroke="currentColor"/>
  <line x1="420" y1="144" x2="640" y2="144" stroke="currentColor"/>
  <line x1="420" y1="166" x2="640" y2="166" stroke="var(--fg-muted)"/>
  <text x="412" y="103" text-anchor="end" fill="currentColor" font-size="8">+3</text>
  <text x="412" y="125" text-anchor="end" fill="currentColor" font-size="8">+1</text>
  <text x="412" y="147" text-anchor="end" fill="currentColor" font-size="8">−1</text>
  <text x="412" y="169" text-anchor="end" fill="currentColor" font-size="8">−3</text>
  <line x1="420" y1="111" x2="640" y2="111" stroke="var(--accent)" stroke-dasharray="3 3"/>
  <line x1="420" y1="133" x2="640" y2="133" stroke="var(--accent)" stroke-dasharray="3 3"/>
  <line x1="420" y1="155" x2="640" y2="155" stroke="var(--accent)" stroke-dasharray="3 3"/>
  <text x="650" y="114" fill="var(--accent)" font-size="7">+2s/3</text>
  <text x="650" y="136" fill="var(--accent)" font-size="7">0</text>
  <text x="650" y="158" fill="var(--accent)" font-size="7">−2s/3</text>
  <text x="200" y="112" fill="currentColor" font-size="8" font-weight="bold">slicer scale s = 2π · 1800 / 48000 rad/sample</text>
  <text x="200" y="128" fill="var(--fg-muted)" font-size="8">nxdn_deviation_hz moves s; a 2400 Hz transmitter</text>
  <text x="200" y="140" fill="var(--fg-muted)" font-size="8">read at 1800 pushes ±1 past ±2s/3 → bimodal dibits</text>
  <text x="200" y="160" fill="var(--fg-muted)" font-size="8">NXDN96 IQ.wav through the pipeline: 3 / 50 / 3 / 44 %</text>
  <text x="200" y="172" fill="var(--fg-muted)" font-size="8">spec-ideal balanced stream: 25 / 25 / 25 / 25 %</text>
</svg>
<figcaption>The chain and its one calibrated number. The slicer's thresholds sit at ±2/3 of a scale derived from the configured deviation; the AGC keeps the matched-filter output at that scale, and a wrong deviation shows up as a lopsided dibit histogram, not as a missing sync.</figcaption>
</figure>

## Frame sync and the 80 ms frame

The Frame Sync Word is 16 bits — 8 dibits — and direction-specific
([FSW reference]({{ '/reference/nxdn-fsw/' | relative_url }})):
`FSWOutboundHex` 0xC55A for base-to-mobile, `FSWInboundHex` 0x3AA5 for
mobile-to-base, decomposed by `hexToDibits` into `FSWDibitsOutbound` and
`FSWDibitsInbound`. The constants' comment is candid about provenance:
they "match the most commonly cited values in public reference
implementations" and "should be cross-checked against the published
technical document before live captures". `nxdn.SyncDetector` slides an
8-dibit ring over the stream, scores every configured pattern, and emits a
`Match{Index, Inbound}` where the best pattern's mismatch count is within
tolerance — `TestSyncDetectorTolerates1Error` and
`TestSyncDetectorRejectsTooManyErrors` bracket it. `ControlChannel.Process`
builds the detector with the outbound pattern only, tolerance 1, and the
`TrafficChannel` of the voice path does the same; the `Inbound` flag exists
so a caller that configures both patterns can ignore uplink bursts, which
never carry the CC announcements the state machine locks on.

The frame itself is in `frame.go`
([frame structure]({{ '/reference/nxdn-frame-structure/' | relative_url }})):

```go
// internal/radio/nxdn/frame.go
const (
    FSWDibits       = 8   // 16 bits
    LICHWireDibits  = 8   // 16 bits transmitted
    SACCHDibits     = 32  // 64 bits
    InfoFieldDibits = 144 // 288 bits — CAC / VCH / UDCH / FACCH
    FrameDibits     = FSWDibits + LICHWireDibits + SACCHDibits + InfoFieldDibits // 192
    FrameDurationMs = 80
)
```

192 dibits at 4800 symbols per second is exactly 80 ms. The layout above is
the traffic (RDCH) shape — FSW, LICH, a 32-dibit SACCH, 144 dibits of
Information field — and `TestFrameLayoutSumsCorrectly` pins the arithmetic.
The control channel's outbound frame has the same length and a different
interior: per NXDN-TS-1-A §4.6, FSW 20 bits + LICH 16 + CAC 300 + E 24 +
Post 24 = 384 bits, so the CAC occupies what a traffic frame spends on
SACCH plus most of the Info field. That is why `process.go`'s spec path
collects `postSyncDibitsViterbiSpec` = 8 + 150 dibits after the FSW and
never looks for a SACCH on the RCCH, while the legacy `ViterbiOff` /
`ViterbiOn` fixture layouts skip 32 SACCH dibits before the CAC. The SACCH
coder in `sacch.go` — 26 payload bits plus a CRC-6, K = 5 rate-½, twelve
punctured positions, a 60-position interleave from dsdcc — exists and is
tested, but the production control path does not read it; neither does the
traffic path, which skips the 32 dibits on its way to the voice frames.

## The LICH

The Link Information Channel is the first thing after sync and the first
place NXDN's coding philosophy shows
([LICH reference]({{ '/reference/nxdn-lich/' | relative_url }})). Eight
information bits: RFCT (0 = RCCH control, 1 = RDCH traffic), two bits of
Function Channel Type (NSACCH, NUDCH, Frame Step, Reserved), two Option
bits, a reserved zero, Direction (0 outbound, 1 inbound) and an even parity
bit over the first seven. On the wire every bit is transmitted twice, so
eight bits become sixteen — `LICHWireDibits` 8 — and the decoder is a
majority vote per pair:

```go
// internal/radio/nxdn/lich.go (shape)
func DecodeLICHWire(wire []byte) (byte, int) {
    for i := 0; i < 8; i++ {
        a, c := wire[2*i]&1, wire[2*i+1]&1
        if a == c { bit = a } else { disagreements++; bit = a } // tie: neither copy is reliable
        b |= bit << uint(7-i)
    }
    return b, disagreements
}
```

A disagreeing pair can only be flagged, not resolved — two copies have no
majority — so `DecodeLICHWire` returns the count and `ParseLICH` then checks
parity, setting `ParityOK`. `TestLICHWireMajorityOnSingleErr` and
`TestLICHParityDetectsSingleError` pin the two halves. The control channel
drops any frame whose LICH fails parity or whose `RFCh` is not
`RFChControl` (`TestControlChannelIgnoresBadParity`,
`TestControlChannelIgnoresTrafficLICH`); the traffic channel inverts the
test and keeps only `RFChTraffic`. That one bit is the FDMA analogue of
DMR's slot type, as
[Protocol Decoders Part 6]({{ '/blog/deep-dives/protocol-decoders-06-nxdn-dpmr/' | relative_url }})
put it — and even on the soft-decision path of Part 9 the LICH is decoded
hard, because, as `tryIngestFrameSoft` says, "its (16, 8) wire code is
trivially strong at any SNR where the FSW correlates".

## The scrambler model

NXDN's optional "encryption" is a scrambler
([reference]({{ '/reference/nxdn-scrambler/' | relative_url }})): a
pseudo-random keystream XORed over the information field, generated by a
15-bit linear-feedback shift register seeded with a 15-bit key. Key 0 is an
all-zero keystream, i.e. clear; the key space is 2¹⁵ = 32 768, which is why
the scrambler comment calls it "trivially brute-forceable" and points at the
offline cryptolab tool that uses this primitive with the frame CRCs as an
oracle. `scramble.go` ships:

```go
// internal/radio/nxdn/scramble.go (shape)
const ScramblerKeyBits = 15
const ScramblerKeyMax  = (1 << ScramblerKeyBits) - 1 // 32767
func (s *Scrambler) Next() byte {
    out := byte((s.state >> 14) & 1)
    fb := ((s.state >> 14) ^ (s.state >> 13)) & 1 // x^15 + x^14 + 1
    s.state = ((s.state << 1) | fb) & ScramblerKeyMax
    return out
}
func Descramble(bits []byte, key uint16) []byte { return Scramble(bits, key) }
```

And then the file does something the series keeps praising: it says what
the code is. "IMPLEMENTATION NOTE — synthetic model, not yet
hardware-confirmed." The tap polynomial and seed mapping are "an
internally-consistent working model" — a maximal-length Fibonacci LFSR whose
output is a balanced m-sequence of period 32 767
(`TestKeystreamIsBalancedMSequence`), self-inverse under XOR
(`TestScrambleRoundTrip`), clear at key 0 (`TestScrambleKeyZeroIsClear`) —
and "when such a capture is available, adjust the feedback in
`(*Scrambler).Next` and the golden expectations in scramble_test.go; nothing
else depends on this choice." The XOR-symmetric structure and the 15-bit key
space are the spec-level facts; the polynomial is a placeholder with a test
that will have to change. That is the honest shape of an unverified
constant, the same posture the voice placeholders of Part 13 take.

## Taps, scope and the two WAVs

The receiver carries the full diagnostic surface the DMR and P25 receivers
have, added in the parity pass `docs/protocol-feature-parity.md` records.
`SoftSink` receives the post-AFC/AGC one-sample-per-symbol soft track,
index-aligned with the dibit batch; `EyeSink` receives the oversampled
matched-filter buffer scaled by that batch's AGC gain and recentred by the
AFC's offset so the four rails line up; `SymbolSink` is kept for API parity
and never fires, since pure C4FM has no complex symbol domain
(`TestReceiverEmitsSoftAndEyeTaps`, `TestReceiverTapsDoNotAlterDecode`).
`AGCLevel`, `AGCTarget`, `MMClockMu` and `MMClockSPS` expose the loop state
for the Tuning panel. On top of that, `internal/scanner/symbolscope` builds
an `nxdn` receiver at `nxdnDeviationHz` 1800 with the same four taps, so an
NXDN rig's web panels open the right receiver instead of a P25 C4FM one —
the `symbol_proto` lesson CLAUDE.md records from a TETRA rig that graded
itself "poor" through a foreign receiver. The scope's NXDN receiver
deliberately runs no AFC and reports `CarrierOffsetHz` 0: the comment
explains that even when enabled it reads through the unit-energy RRC's
scale mismatch, so the honest panel shows AGC, clock, constellation, eye and
dibits and no offset — "exactly the DMR posture".

Which brings the rung. Every test above is synthetic: `makePhaseRampIQ`
cycles the four symbol values, `makeRunC4FMIQ` holds them in runs,
`makeC4FMIQWithOffset` rotates the whole buffer, and
`TestDaemonCCDecodesNXDN` boots the daemon on `demod.ModulateC4FM` output
at 851.0625 MHz, 48 kHz, 1800 Hz deviation, twenty spec-encoded frames.
The receiver has never decoded a frame of real air. The two committed
captures in `samples/nxdn/` are IQ and real, and `samples/README.md` reports
exactly what the chain makes of them through `samples/cmd/audio_smoketest`
(`-protocol nxdn` runs its IQ path): `NXDN48 IQ.wav` slices to a balanced
26 / 27 / 15 / 32 % and finds **no FSW** — "likely 4800-bps BFSK rather than
9600 4-FSK", the variant the receiver does not have; `NXDN96 IQ.wav` slices
**bimodal, 3 / 50 / 3 / 44 %** — outer ±3 dominating, inner ±1 starved —
"consistent with a different deviation than the spec value". That second
number is the whole reason `nxdn_deviation_hz` exists: `samples/nxdn/README.md`
says non-spec transmitters "land between 2200 and 2700 Hz" and gives the
recipe — print the dibit distribution, sweep the key until it flattens
toward 25 / 25 / 25 / 25 %, then pin it. Nobody has yet reported the value
that flattens that file.

`docs/decoder-capture-needs.md` files NXDN in **Tier 1 — blocking**: an
outbound RCCH `.cfile` at ≥ 48 kHz, ≥ 5 s (10 is better), with an MMDVMHost
log or DSDcc as the cross-check, passing at ≥ 80 % CRC-OK CAC bursts, a
byte-match on SystemID / SiteID / RAN, and lock within 3 s. The harness is
already written: `TestDaemonCCDecodesNXDNRealAir` skips with a documented
message until exactly one `*.cfile` + `*.metadata.json` pair lands in
`samples/nxdn/`, then boots the production `newNXDNPipeline` against it and
asserts `LockState.SystemID` / `SiteID` / `FrequencyHz` against the sidecar.
Until that file exists, NXDN's physical layer stands on the ladder's lowest
real rung: **reference-shaped, synthetic-verified, with two real captures
that prove only what it cannot yet do.**

### How NXDN's physical layer shaped the Go code

- **Calibration is opt-in by value.** `DeviationHz <= 0` keeps
  `slicerScale` 1.0 and disables the AGC (`C4FMAGCTarget` returns 0), so
  every pre-scaled fixture is byte-identical.
- **Taps are nil-safe and never alter the decode.** Each sink is checked
  before use; `TestReceiverTapsDoNotAlterDecode` pins the dibit stream with
  and without them.
- **Direction is a field on the match.** `Match.Inbound` lets one detector
  serve both patterns while `Process` honours only outbound hits.
- **Unverified constants say so in the file.** The FSW comment, the
  scrambler note and the `SymbolToDibit` pin each name the capture that
  would confirm them.

## Where this goes next

Dibits are not a control channel. The 150 dibits after an outbound LICH
carry 300 channel bits that must be deinterleaved 25 × 12, depunctured
50 / 350, Viterbi-decoded at K = 5 and CRC-checked before a single RCCH
message exists.
[Part 9]({{ '/blog/deep-dives/legacy-family-09-nxdn-cac-and-trunking/' | relative_url }})
walks that chain, the `ViterbiSpec` / `ViterbiOn` / `ViterbiOff` modes, the
soft-decision lever that lifts CAC CRC yield from 26/200 to 151/200 on the
synthetic channel, and the data-mean drift that keeps `nxdn_afc` off by
default.

## FAQ

**Which NXDN variant does GopherTrunk decode?**
The 9600 bps 4-FSK variant (NXDN96, 12.5 kHz): `newNXDNPipeline` constructs
the control channel with `nxdn.Rate9600`, and `internal/radio/nxdn/receiver`
is a 4-level C4FM chain. The 4800 bps BFSK variant (NXDN48, 6.25 kHz) needs
a 2-level receiver that is a documented follow-up, which is why the committed
`NXDN48 IQ.wav` finds no FSW.

**What does `nxdn_deviation_hz` change?**
The slicer's calibration: `slicerScale = 2π·DeviationHz/SampleRateHz`, with
the 4-level thresholds at ±2/3 of it and the symbol AGC targeting it. The
default 1800 Hz is the Common Air Interface value. A transmitter at a
different deviation produces a bimodal dibit histogram — the `NXDN96 IQ.wav`
sample reads 3 / 50 / 3 / 44 % — and non-spec rigs typically land at
2200–2700 Hz.

**Why does the NXDN receiver need a symbol AGC?**
Because its RRC matched filter is normalised to unit energy, which has a DC
gain of about 3.1 on the rectangular symbols a real transmitter sends. The
4-level eye then sits ~3.1× above the slicer's fixed thresholds, inner ±1
symbols slice as ±3, and payloads fail while the FSW still matches.
`demod.C4FMSymbolAGC` renormalises the level; `TestReceiverDecodesOverScaledC4FM`
pins it.

**How is the NXDN LICH decoded?**
Its 8 information bits are each transmitted twice. `DecodeLICHWire` takes
the 16 wire bits, majority-votes each pair (a disagreement is counted, not
resolved), and `ParseLICH` checks even parity over bits 0–6. The RF
channel-type bit routes the frame: `RFChControl` frames go to the CAC
parser, `RFChTraffic` frames to the voice path.

**Is the NXDN scrambler implementation confirmed?**
No. `scramble.go` models it as a maximal-length 15-bit Fibonacci LFSR with
feedback x¹⁵ + x¹⁴ + 1 and a 32 768-key space, and labels that a synthetic,
not hardware-confirmed model. The XOR-symmetric structure and key width are
the spec-level facts; the polynomial and seed mapping await a known-key
capture, and the file names the two places to change when one lands.

## Series navigation

**Part 8 of 14** · ←
[Part 7: Analog Voice on Trunked FM — From a Grant to the Composer's FM Chain]({{ '/blog/deep-dives/legacy-family-07-analog-voice-on-trunked-fm/' | relative_url }})
· Next →
[Part 9: NXDN CAC and Trunking — Interleaver, Puncture, Viterbi and the Soft-Decision Lever]({{ '/blog/deep-dives/legacy-family-09-nxdn-cac-and-trunking/' | relative_url }})
