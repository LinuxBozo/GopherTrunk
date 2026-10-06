---
title: "The Legacy Family End to End, Part 6: MPT 1327 — FFSK Codewords, CWSC Tolerance and BCH(64,48)"
description: "How GopherTrunk decodes an MPT 1327 control channel: 1200-baud FFSK through an FM discriminator, the 16-bit Codeword Synchronisation Code matched at a 2-bit Hamming tolerance, the 64-bit codeword's BCH(64,48) single-error correction, the Aloha / AHYC / GTC opcodes — and exactly which of those layers a real recording has confirmed."
category: deep-dives
keywords: mpt 1327 decoder, mpt1327 ffsk 1200 baud, codeword synchronisation code cwsc, mpt1327_cwsc_tolerance, bch 64 48 codeword, mpt 1327 go to channel gtc, aloha codeword, mpt1327_bch_mode, mpt 1327 sdr, gophertrunk legacy family end to end
tags: [legacy-family-end-to-end, mpt1327, ffsk, bch, trunking, go]
author: Matt Cheramie
image: /assets/gophertrunk-logo.png
series: "The Legacy Family End to End"
series_part: 6
---

*Part 6 of **The Legacy Family End to End**, a 14-part deep dive through
the protocols the P25, DMR and TETRA series left out — the FM-era trunking
generation and the AMBE-era narrowband and amateur modes — with each
protocol placed honestly on one verification ladder.
[Part 5]({{ '/blog/deep-dives/legacy-family-05-ltr/' | relative_url }})
followed LTR, the system with no control channel at all. This part takes
the last of the FM-era four: MPT 1327, whose control channel is a stream of
64-bit codewords riding 1200-baud FFSK tones inside an ordinary narrowband
FM channel — and the one legacy decoder in the tree that has decoded a
recording of a real transmitter.*

> **TL;DR:** `internal/radio/mpt1327` decodes the MPT 1327 control channel
> in three layers. The receiver (`mpt1327/receiver`) runs `demod.FM` →
> `demod.FFSK` (mark 1200 Hz = 1, space 1800 Hz = 0) → `sync.MuellerMuller`
> at the 48 kHz `ddcTargetRateHz` and emits raw bits through a `BitSink`.
> `ControlChannel.Process` aligns the stream on the 16-bit Codeword
> Synchronisation Code `1100010011010111` (`cwscPattern`, 0xC4D7) within
> `mpt1327_cwsc_tolerance` bit errors (default `cwscDefaultMaxErrors` = 2),
> then slices 64-bit codewords and runs each through
> `framing.BCHDecodeMPT1327`: 48 info bits, a 15-bit check from generator
> 0x6815 seeded 0x0001, one overall parity bit, single-error correction.
> `Ingest` locks on an Aloha or AHYC only after `mpt1327ProdMinConfirm` = 2
> codewords sharing one Prefix, and republishes a GTC as a
> `trunking.Grant` with `Protocol "mpt1327"`. Rung: the codeword, CWSC and
> BCH layers are **capture-pinned on real audio** — the two committed
> `samples/mpt1327/*.mp3` recordings decode through the manual
> `samples/cmd/audio_smoketest` harness (7 cc.locked + 4 grants, SystemID
> 0x1fd7 on the second); the IQ receiver is synthetic-verified; and no
> daemon has followed a live MPT 1327 call, because the production pipeline
> installs no band-plan resolver.

**Key takeaways**

- **FFSK is audio-band, so an audio recording is real evidence here.**
  The tones survive FM demodulation; an MP3 of the discriminator output is
  one stage downstream of the receiver's first block, not a lost
  constellation. That is why MPT 1327 is the one legacy protocol with a
  real-transmitter decode in the tree.
- **The sync match is deliberately loose because the codeword check is
  tight.** A 2-of-16 Hamming tolerance accepts ~0.21 % of random windows;
  the BCH(64,48) check that must follow rejects ~2⁻¹⁵ of them, so the
  per-position false-lock rate stays under 1e-7.
- **Two codewords with one Prefix make a lock, never one.** A single
  recognised codeword is what a cross-protocol false parse looks like;
  `SetMinConfirm(2)` and the Prefix-agreement rule stop it.
- **A grant without a frequency is dropped before the engine tunes
  anything.** `newMPT1327Pipeline` passes no `Resolver`, so every live GTC
  carries `FrequencyHz` 0 — the gap
  [Part 7]({{ '/blog/deep-dives/legacy-family-07-analog-voice-on-trunked-fm/' | relative_url }})
  measures.

## Cheat sheet

| Concern | What it does | Where it lives |
|---|---|---|
| IQ → bits | FM discriminator → FFSK tone discriminator (1200 / 1800 Hz) → Mueller-Müller at 40 sps → 2-level slice | `internal/radio/mpt1327/receiver/receiver.go` (`SymbolRate`, `MarkHz`, `SpaceHz`) |
| Sync | 16-bit CWSC `1100010011010111` at Hamming ≤ `cwscDefaultMaxErrors` (2) | `process.go` (`cwscPattern`, `findCWSC`), key `mpt1327_cwsc_tolerance` |
| Codeword check | 48 info + 15-bit BCH (gen 0x6815, init 0x0001) + even parity; single-error correct | `internal/radio/framing/bch_mpt1327.go` (`BCHEncodeMPT1327`, `BCHDecodeMPT1327`) |
| Fields | Type 1 · Prefix 7 · Ident 13 · Op 10 · Function 17; Kind = Function ≫ 13 | `codeword.go` (`CodewordFromBits48`), `opcodes.go` (`CodewordKind`) |
| Lock | Aloha / AHYC, after 2 recognised codewords on one Prefix | `control.go` (`noteConfirmation`, `mpt1327ProdMinConfirm`) |
| Grant | GTC → `GroupID` = Prefix ≪ 16 ‖ Ident, `ChannelNum`, `FrequencyHz` via `Resolver` (none installed in production) | `control.go` (`publishGrant`), `bandplan.go` |
| Pins | `process_cwsc_test.go`, `process_bch_test.go`, `minconfirm_test.go`, `TestDaemonCCDecodesMPT1327` | `internal/radio/mpt1327/`, `cmd/gophertrunk/integration_cc_mpt1327_test.go` |
| Real audio | two sigidwiki MP3s → ffmpeg → FFSK chain → `ControlChannel` (manual) | `samples/cmd/audio_smoketest/main.go`, `samples/README.md` |

## In this post

- **Tones inside an FM channel** — the receiver, and why 48 kHz is more than enough.
- **The 64-bit codeword** — fields, the BCH(64,48) primitive and its one deliberate ambiguity.
- **Finding the codeword boundary** — CWSC, tolerance, the false-lock arithmetic and the fallback.
- **From codeword to event** — Kind dispatch, the two-codeword lock, and the grant's missing frequency.
- **Where MPT 1327 stands on the ladder** — what real audio proved, and what no capture has.

## Tones inside an FM channel

MPT 1327 ([reference]({{ '/reference/mpt-1327/' | relative_url }})) is the
1988 UK Code of Practice still carrying taxi, transport and utility fleets.
Its control channel is **continuous 1200-baud CCIR FFSK** — a 1200 Hz tone
for a binary 1 and an 1800 Hz tone for a 0 — modulated as ordinary audio on
a 12.5 kHz NBFM carrier ([FFSK]({{ '/reference/ffsk/' | relative_url }})).
That one physical fact shapes everything in the package. The information
lives in the audio band, so the receiver is the shortest in the legacy
family and an audio recording of the discriminator is not a degraded
capture — it is the signal the second stage expects.

`internal/radio/mpt1327/receiver` composes exactly that:

```go
// internal/radio/mpt1327/receiver/receiver.go (shape)
const (
    SymbolRate = 1200.0
    MarkHz     = 1200.0 // binary 1
    SpaceHz    = 1800.0 // binary 0
)
r.disc    = r.fm.Process(r.disc, iq)            // demod.FM
r.tone    = r.ffsk.Discriminate(r.tone, r.disc) // demod.FFSK(rate, 1200, 1800)
r.symbols = r.clock.Process(r.symbols, r.tone)  // sync.MuellerMuller(sps, 0.05)
for i, s := range r.symbols { r.bits[i] = byte(r.ffsk.Slice(s)) }
r.bitSink(r.bits, r.bitBase)
```

`New` panics below 3600 Hz (twice the space tone) and below two samples per
symbol; the production pipeline hands it the C4FM family's 48 kHz
`ddcTargetRateHz` from `ddcTargetForProtocol`, which is 40 samples per
symbol — far more than the loop needs, but the DDC's anti-alias filter is
the channel filter, and the shared target keeps the MPT path on the same
down-converter as
[Part 2]({{ '/blog/deep-dives/legacy-family-02-the-shared-skeleton/' | relative_url }})
described. `Reset` rewinds the `BitSink` base index to 0 and clears the
FFSK mixer phase and low-pass history, so a retune produces a fresh
baseline.

The receiver's one load-bearing test is `TestReceiverRecoversTransmittedBits`:
a 1010… preamble, a 36-bit distinctive payload and a trailing flush, FM- and
FFSK-modulated by the test's own `makeFMFFSKIQ` at 4 kHz deviation, must come
back bit-exact at the best alignment (one slip allowed at the clock-recovery
edges). Its comment names why the older emits-some-symbols test was not
enough: a demodulator that produces a plausible but wrong 2-level stream
"would emit bits but never align to the payload" — the failure mode the
test cites from issue
[#927](https://github.com/MattCheramie/GopherTrunk/issues/927). It is still
a self-generated fixture; the independent evidence for this stage comes at
the end of the post.

## The 64-bit codeword

An MPT 1327 address codeword is 64 bits on the wire
([codeword reference]({{ '/reference/mpt-1327-codeword/' | relative_url }})):
a 48-bit information field, a 15-bit check and one overall parity bit.
`codeword.go` models the information field two ways — a legacy 38-bit
layout the older fixtures use (`AssembleCodeword` / `ParseCodeword`, no
`Op`), and the spec-complete 48-bit layout the BCH path populates:

```go
// internal/radio/mpt1327/codeword.go — 48-bit information field
//  bit 47      Type     0 = address codeword, 1 = data codeword
//  bits 46..40 Prefix   7-bit area / system prefix
//  bits 39..27 Ident    13-bit radio or fleet identity
//  bits 26..17 Op       10-bit operation field
//  bits 16..0  Function 17-bit opcode-specific information
func (c Codeword) Kind() CodewordKind { return CodewordKind((c.Function >> 13) & 0xF) }
func (c Codeword) FunctionPayload() uint16 { return uint16(c.Function & 0x1FFF) }
```

The upper four bits of Function are the spec's Address Categorisation
subfield, and `opcodes.go` names the ones the trunking layer acts on:
`KindAloha` 0x1 (ALH, the idle beacon), `KindAhoy` 0x2 (AHY), `KindAhoyChan`
0x3 (AHYC, the broadcast whose 13-bit payload the package treats as a system
identifier), `KindGoToChan` 0x4 (GTC, the grant whose payload is the channel
number), `KindAck` 0x5, `KindDisconnect` 0x6 (DUL), `KindData` 0x7 and
`KindEmergency` 0xE. `AsGoToChannel` and `AsAhoyChannel` only answer for
`TypeAddress` codewords of the right Kind.

The check is `framing.BCHEncodeMPT1327` / `BCHDecodeMPT1327`
([BCH]({{ '/reference/bch-code/' | relative_url }})). Its header credits
the layout to SDRTrunk's `CRCFleetsync` implementation — FleetSync and
MPT 1327 share it — so this is a **reference-pinned** primitive, not a
spec transcription:

```go
// internal/radio/framing/bch_mpt1327.go (shape)
// bits 0..47 info · bits 48..62 15-bit check · bit 63 even parity over the 63-bit body
// g(x) = x^15 + x^14 + x^13 + x^11 + x^4 + x^2 + 1
const (
    bchMPT1327PolyHigh uint16 = 0x6815 // generator without the implicit x^15
    bchMPT1327Init     uint16 = 0x0001 // seed, so the all-zero codeword is not trivially valid
)
var bchMPT1327Syndromes [48]uint16 // x^i mod g(x), built at init
```

Decode recomputes the check and parity. Both match: `errs` 0. Check matches
but parity does not: the parity bit itself flipped, `errs` 1. Otherwise the
syndrome is matched against the 48 info columns and the 15 check positions,
and a single error is corrected only if the parity *also* disagrees — a
syndrome mismatch with matching parity means at least two errors, `errs` −1,
and the adapter drops the window. One ambiguity is documented rather than
hidden: a check bit at offset k carries the same syndrome `1 << k` as info
bit k for k = 0..14, so the decoder prefers the info-bit correction, and
`TestBCHMPT1327CorrectsAnySingleBitError` requires exact info recovery for
positions 0..47 and 63 but only *detection* for 48..62. Note the comments
drift in what they call the code — `BCH(63,38)` in the package header,
`BCH(64,48,2)` at the primitive — and the primitive is what runs: 48 + 15 + 1.

<figure class="lab-figure">
<svg viewBox="0 0 680 170" width="680" height="170" role="img" aria-label="Bit layout of one 64-bit MPT 1327 codeword as the BCH primitive sees it: a 48-bit information field split into Type 1 bit, Prefix 7, Ident 13, Op 10 and Function 17, where the top four Function bits are the Kind; then a 15-bit BCH check from generator 0x6815 seeded 0x0001, then one overall even parity bit. Below, the 16-bit CWSC 1100010011010111 is shown preceding the first codeword of a message.">
  <text x="340" y="14" text-anchor="middle" fill="currentColor" font-size="10" font-weight="bold">64-bit codeword · 48 info + 15 check + 1 parity</text>
  <rect x="20" y="28" width="12" height="34" fill="none" stroke="currentColor"/>
  <text x="26" y="76" text-anchor="middle" fill="var(--fg-muted)" font-size="8">T</text>
  <rect x="32" y="28" width="56" height="34" fill="none" stroke="currentColor"/>
  <text x="60" y="48" text-anchor="middle" fill="currentColor" font-size="9">Prefix</text>
  <text x="60" y="76" text-anchor="middle" fill="var(--fg-muted)" font-size="8">7</text>
  <rect x="88" y="28" width="104" height="34" fill="none" stroke="currentColor"/>
  <text x="140" y="48" text-anchor="middle" fill="currentColor" font-size="9">Ident</text>
  <text x="140" y="76" text-anchor="middle" fill="var(--fg-muted)" font-size="8">13</text>
  <rect x="192" y="28" width="80" height="34" fill="none" stroke="currentColor"/>
  <text x="232" y="48" text-anchor="middle" fill="currentColor" font-size="9">Op</text>
  <text x="232" y="76" text-anchor="middle" fill="var(--fg-muted)" font-size="8">10</text>
  <rect x="272" y="28" width="136" height="34" fill="none" stroke="currentColor"/>
  <rect x="272" y="28" width="32" height="34" fill="none" stroke="var(--accent)" stroke-dasharray="3 2"/>
  <text x="288" y="48" text-anchor="middle" fill="var(--accent)" font-size="8">Kind</text>
  <text x="356" y="48" text-anchor="middle" fill="currentColor" font-size="9">Function</text>
  <text x="340" y="76" text-anchor="middle" fill="var(--fg-muted)" font-size="8">17 (top 4 = Kind, low 13 = payload)</text>
  <rect x="408" y="28" width="120" height="34" fill="none" stroke="var(--accent)"/>
  <text x="468" y="48" text-anchor="middle" fill="var(--accent)" font-size="9">BCH check</text>
  <text x="468" y="76" text-anchor="middle" fill="var(--fg-muted)" font-size="8">15 · g = 0x6815 · init 0x0001</text>
  <rect x="528" y="28" width="12" height="34" fill="none" stroke="var(--accent)"/>
  <text x="534" y="76" text-anchor="middle" fill="var(--fg-muted)" font-size="8">P</text>
  <text x="600" y="48" text-anchor="middle" fill="var(--fg-muted)" font-size="8">single-error correct</text>
  <text x="600" y="60" text-anchor="middle" fill="var(--fg-muted)" font-size="8">double-error detect</text>
  <line x1="20" y1="100" x2="660" y2="100" stroke="var(--fg-muted)" stroke-dasharray="2 3"/>
  <rect x="20" y="112" width="128" height="30" fill="none" stroke="var(--accent)"/>
  <text x="84" y="131" text-anchor="middle" fill="var(--accent)" font-size="9">CWSC 1100010011010111</text>
  <rect x="148" y="112" width="200" height="30" fill="none" stroke="currentColor"/>
  <text x="248" y="131" text-anchor="middle" fill="currentColor" font-size="9">codeword 1 (always Address)</text>
  <rect x="348" y="112" width="200" height="30" fill="none" stroke="currentColor"/>
  <text x="448" y="131" text-anchor="middle" fill="currentColor" font-size="9">codeword 2 …</text>
  <text x="604" y="131" text-anchor="middle" fill="var(--fg-muted)" font-size="8">64 bits each</text>
  <text x="340" y="160" text-anchor="middle" fill="var(--fg-muted)" font-size="8">a message: the 16-bit sync, then back-to-back 64-bit codewords at 1200 bit/s</text>
</svg>
<figcaption>The 48-bit information field the BCH path recovers, with the Kind nibble sitting in the top of Function; the 16-bit CWSC precedes the first codeword of every message and is the stream's only fixed pattern.</figcaption>
</figure>

## Finding the codeword boundary

MPT 1327 has no frame sync word in the P25 sense. What it has is the
**Codeword Synchronisation Code** — `1100010011010111`, 0xC4D7 MSB-first —
transmitted immediately before the first codeword of every message, and
`process.go` keeps it as a 16-entry byte array (`cwscPattern`) so the
matcher's hot loop compares bit-by-bit against the receiver's one-bit-per-byte
stream without packing. `findCWSC(buf, from, maxErrors)` returns the first
window whose Hamming distance is at most `maxErrors`:

```go
// internal/radio/mpt1327/process.go (shape)
const cwscDefaultMaxErrors = 2 // of 16 — matches commercial MPT 1327 receivers
for i := from; i <= end; i++ {
    errs := 0
    for j := 0; j < cwscBits; j++ {
        if buf[i+j]&1 != cwscPattern[j] { if errs++; errs > maxErrors { break } }
    }
    if errs <= maxErrors { return i, true }
}
```

The tolerance is a trade the code states in numbers. With two errors
allowed, a random 16-bit window matches with probability
C(16,0)+C(16,1)+C(16,2) = 1 + 16 + 120 = 137 / 65536 ≈ 0.21 %;
`TestFindCWSCFalsePositiveControl` draws 65 536 random windows and fails
above a 0.5 % ceiling. But a CWSC match alone never produces an event — the
64-bit window after it must pass `BCHDecodeMPT1327`, whose random-pass rate
the comments put at roughly 2⁻¹⁵, which keeps the combined per-bit-position
false-lock rate under 1e-7. The whole table is exercised by
`TestFindCWSCWithinTolerance`: zero, one and two flips match at tolerance 2,
three do not; exact match accepts only the clean pattern.

Alignment in `Process` is two-stage. The adapter appends each chunk to a
cross-call buffer, and while unaligned it tries the CWSC first: on a hit it
locks at the bit after the sync, parses that codeword, and consumes forward
at a fixed 64-bit stride (38 under `BCHOff`). Only if no CWSC is in the
buffer does it fall back to the legacy search — slide one bit at a time
until a window both passes the check and parses as a recognised Address
codeword (`isRecognisedAddressCodeword`). Aligned, every frame that fails
the recognised-codeword test counts against `maxConsecBad` = 8; at eight it
drops alignment and restarts the search one bit *after* the failed frame, so
it cannot immediately re-lock to the same wrong offset. The tail of the
buffer is kept so a codeword straddling a chunk boundary parses on the next
call (`TestProcessHandlesCodewordSpanningCalls`).

Both knobs reach the operator through the system config. `mpt1327_bch_mode`
(`ParseBCHMode`: empty or `on` → `BCHOn`, `off` → the 38-bit pre-stripped
path for synthesised fixtures) and `mpt1327_cwsc_tolerance`
(`ParseCWSCTolerance`: empty → 2, `exact` / `off` / `0` → exact, an integer
in [0, 15] otherwise; `SetCWSCTolerance` clamps anything ≥ `cwscBits` to 15).
Both parsers return `ok = false` on nonsense so `newMPT1327Pipeline` can warn
and fall back rather than silently decoding in the wrong mode.

## From codeword to event

`Ingest` is the state machine, and it is small. Data codewords
(`TypeData`) are dropped — short messages are not followed at the trunking
layer. Under `SetStrictValidation(true)` anything outside the recognised
Kind set is dropped too (`TestStrictValidationDropsUnknownKind`). Then the
Kind decides: Aloha and AHYC are lock evidence, GTC is a grant.

The lock has a discipline the other legacy packages lack. A real MPT 1327
control channel streams codewords continuously, so demanding two costs
nothing — while one recognised codeword is exactly what an off-channel P25
or DMR carrier handed to the identifier can produce by chance.
`noteConfirmation` counts recognised Address codewords and requires them to
share one 7-bit Prefix; a different Prefix restarts the count at one.
`newMPT1327Pipeline` sets `SetMinConfirm(mpt1327ProdMinConfirm)` = 2 for
production, while `New` zero-values to lock-on-first so the in-package
fixtures still lock. `minconfirm_test.go` pins all four shapes: one
codeword does not lock, two do, 0x3 then 0x5 does not, and a 1,2,1,2,…
alternation never does (`TestMinConfirmAlternatingIdentityNeverLocks`).

The `LockState` carries `FrequencyHz`, the AHYC payload as `SystemID`, and
the `Prefix`; `LockedNAC` returns the SystemID because the cchunt supervisor
type-asserts `trunking.LockedPayload` on every cc.locked and would otherwise
silently drop the event. `maybeLock` re-publishes only when the state
changes, so a channel alternating Aloha and AHYC does not flap.

A GTC becomes a grant through `publishGrant`:

```go
// internal/radio/mpt1327/control.go (shape)
groupID := uint32(g.Prefix)<<16 | uint32(g.Ident) // (Prefix, Ident) is the called party
c.bus.Publish(events.Event{Kind: events.KindGrant, Payload: trunking.Grant{
    System: c.systemName, Protocol: "mpt1327",
    GroupID: groupID, FrequencyHz: freq, ChannelNum: g.Channel, At: c.now(),
}})
```

`freq` comes from `Options.Resolver` — `LinearBandPlan` (base + (channel +
offset) × spacing) or `TableBandPlan` in `bandplan.go`, because MPT 1327
channel numbering is system-specific. And here is the gap the series has to
state plainly: **`newMPT1327Pipeline` passes no `Resolver`**, and no
`mpt1327_*` band-plan key exists in the config, so a live GTC is published
with `FrequencyHz` 0 — exactly what
`TestControlChannelGrantWithoutResolverHasZeroFreq` pins — and
`Engine.HandleGrant` drops it with `dropping grant with zero frequency`
before any tuner moves. The codeword layer is complete; the follow-the-call
layer is not wired.

## Where MPT 1327 stands on the ladder

Three kinds of evidence exist, and they pin different layers.

**Synthetic, in CI.** `process_bch_test.go` encodes codewords with the
framing primitive and feeds the 64-bit wire form to `Process(BCHOn)`:
`TestProcessBCHOnDecodesEncodedCodeword` (Aloha then GTC → lock and a
`Protocol "mpt1327"` grant for channel 7),
`TestProcessBCHOnCorrectsSingleBitError` (bit 35 flipped inside Function,
still locks) and `TestProcessBCHOnDropsUncorrectableCodeword` (bits 20 and
33 flipped, no lock). `TestDaemonCCDecodesMPT1327` boots the whole daemon on
a mock SDR playing `demod.ModulateFFSK` output — 100 Aloha codewords at
48 kHz on 169.2125 MHz — and asserts the production pipeline, supervisor,
API and metrics recover the lock. Every one of these shares its encoder with
the decoder; they catch asymmetric bugs and nothing shared, the
[self-consistent trap]({{ '/blog/solution-postmortem/from-the-issue-tracker-20-self-consistent-trap/' | relative_url }}).

**Real audio, by hand.** `samples/mpt1327/` holds two sigidwiki MP3
recordings of a 423.6 MHz control channel — FM-demodulated audio, which for
FFSK is the right stage. `samples/cmd/audio_smoketest` shells out to ffmpeg
for 8 kHz float PCM, runs `demod.FFSK` → `sync.MuellerMuller` → slice —
step 2 onward of the production receiver — and drives a `ControlChannel`
with `SetBCHMode(BCHOn)`. `samples/README.md` records the result:
`MPT1327_423.6_1.mp3` gives 7 cc.locked and 1 grant, BCH-verified;
`MPT1327_423.6_2.mp3` gives 7 cc.locked and 4 grants with SystemID
`0x1fd7`. That is a real transmitter's CWSC, codeword layout and BCH
agreeing with the decoder — independent evidence no round-trip can forge.
Its limits are equally real: the harness bypasses `demod.FM`, it constructs
the channel directly (no `SetMinConfirm`, so the seven locks include
state-change republishes), and it is not a test — nothing in CI replays
those files.

**On air, nothing.** No daemon run has followed an MPT 1327 call, and none
can until a resolver is wired. `docs/decoder-capture-needs.md` files MPT 1327
under Tier 3 — "pipeline closed, captures only add robustness" — asking for
≥ 60 s of 48 kHz IQ or 8 kHz audio to measure the empirical CWSC
false-positive rate and per-vendor sync bit-error patterns, and
`samples/mpt1327/README.md` sets the acceptance bar: no spurious locks in
clean traffic, ≥ 95 % of messages producing a lock or grant at tolerance 2,
and a tolerance sweep over {0, 1, 2, 3} that is monotone non-decreasing.

So the honest rung: **codeword / CWSC / BCH layer capture-pinned on real
audio (manual harness); IQ receiver and daemon path synthetic-verified; call
follow unwired.**

### How MPT 1327 shaped the Go code

- **A bit-per-byte sync table.** `cwscPattern` is `[16]byte`, not a
  `uint16`, so the matcher never packs the receiver's stream.
- **Two information layouts, one struct.** `Codeword` carries `Op` only on
  the 48-bit path; the 38-bit helpers ignore it
  (`TestLegacy38BitHelpersIgnoreOp`) so older fixtures stay green.
- **Config parsers return `ok`.** `ParseBCHMode` / `ParseCWSCTolerance`
  map the empty string to the production default and flag garbage, so the
  pipeline warns instead of guessing.
- **Lock confirmation lives in the channel, not the pipeline.** `minConfirm`
  defaults to lock-on-first for direct callers; the connector raises it.

## Where this goes next

Every FM-era decoder in Parts 3–6 ends the same way: a `trunking.Grant` with
a channel number, and a frequency only if someone resolved it.
[Part 7]({{ '/blog/deep-dives/legacy-family-07-analog-voice-on-trunked-fm/' | relative_url }})
follows that grant into the engine and the composer's `runFMChain` — the
analog voice path, its hangtime, the recorder — and measures which of the
four protocols can actually reach it today.

## FAQ

**What does `mpt1327_cwsc_tolerance` do and why is the default 2?**
It is the Hamming distance `findCWSC` accepts between a 16-bit window and
the Codeword Synchronisation Code `1100010011010111`. Two errors out of
sixteen matches commercial MPT 1327 receivers on noisy air; the 64-bit
codeword that follows must still pass `BCHDecodeMPT1327`, so the loose sync
does not loosen frame acceptance. Set `0` or `exact` for pre-stripped
synthesised fixtures.

**Does GopherTrunk correct errors in MPT 1327 codewords?**
Yes, one bit per 64-bit codeword. `framing.BCHDecodeMPT1327` recomputes the
15-bit check (generator 0x6815, seed 0x0001) and the overall parity;
a single flipped bit changes both, and the syndrome names its position.
A syndrome mismatch with matching parity means two or more errors, and the
codeword is dropped rather than guessed.

**Can an MP3 of an MPT 1327 control channel be decoded?**
Yes — FFSK rides audio-band tones, so FM-demodulated audio is the input the
`demod.FFSK` stage expects. The two `samples/mpt1327/*.mp3` recordings
decode through `samples/cmd/audio_smoketest` to 7 cc.locked and 1 or 4
grants. The same is not true of NXDN, TETRA or DMR, whose constellations
are lost in FM demodulation.

**Why does an MPT 1327 lock need two codewords?**
Because one recognised codeword is what a cross-protocol false parse looks
like. `noteConfirmation` requires `mpt1327ProdMinConfirm` = 2 recognised
Address codewords sharing one 7-bit Prefix before `cc.locked` is published;
a real control channel streams codewords continuously, so the extra
confirmation costs negligible latency.

**Is MPT 1327 verified on air?**
Not end to end. Its codeword, CWSC and BCH layers are capture-pinned on real
demodulated audio; the IQ receiver and daemon pipeline are synthetic-verified
(`TestDaemonCCDecodesMPT1327`); and no live call has been followed, because
`newMPT1327Pipeline` installs no band-plan resolver, so every GTC grant
carries frequency 0 and the engine drops it.

## Series navigation

**Part 6 of 14** · ←
[Part 5: LTR — Subaudible Data on Every Repeater, No Control Channel]({{ '/blog/deep-dives/legacy-family-05-ltr/' | relative_url }})
· Next →
[Part 7: Analog Voice on Trunked FM — From a Grant to the Composer's FM Chain]({{ '/blog/deep-dives/legacy-family-07-analog-voice-on-trunked-fm/' | relative_url }})
