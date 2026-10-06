---
title: "The Legacy Family End to End, Part 7: Analog Voice on Trunked FM — From a Grant to the Composer's FM Chain"
description: "What happens after a SmartNet, EDACS, LTR or MPT 1327 decoder publishes a grant: the band-plan resolver that must turn a channel number into hertz, the engine's zero-frequency drop and voice-pool bind, the composer's runFMChain stages and their config keys, how an analog call ends with no release message, and which of the four protocols can reach the recorder today."
category: deep-dives
keywords: analog trunking voice sdr, smartnet voice recording, edacs analog voice, ltr voice follow, runFMChain composer, analog fm call hangtime, band plan resolver lcn to frequency, dropping grant with zero frequency, fm_channel_bandwidth_hz, gophertrunk legacy family end to end
tags: [legacy-family-end-to-end, analog, fm, composer, trunking, go]
author: Matt Cheramie
image: /assets/gophertrunk-logo.png
series: "The Legacy Family End to End"
series_part: 7
---

*Part 7 of **The Legacy Family End to End**, a 14-part deep dive through
the protocols the P25, DMR and TETRA series left out, each placed honestly
on one verification ladder.
[Part 6]({{ '/blog/deep-dives/legacy-family-06-mpt1327/' | relative_url }})
closed the control-channel half of the FM-era four with MPT 1327's
codewords, and ended on a grant carrying `FrequencyHz` 0. This part follows
a grant from any of the four decoders through the trunking engine into the
composer's analog chain and out to a WAV — and measures, protocol by
protocol, which grants can make that journey on a live daemon today.*

> **TL;DR:** Every FM-era decoder ends in `publishGrant` with a channel
> number and a frequency it had to resolve itself. Motorola resolves through
> its built-in `BandPlan` tables (`motorola_band_plan`: `800_standard`,
> `800_rebanded`, `800_splinter`, `900`) and returns silently when the
> command is not a channel; EDACS, LTR and MPT 1327 expose a `Resolver`
> (`LinearBandPlan` / `TableBandPlan`) that `newEDACSPipeline`,
> `newLTRPipeline` and `newMPT1327Pipeline` never install, and no config key
> exists for one — so their live grants carry `FrequencyHz` 0 and
> `Engine.HandleGrant` drops them first thing (`dropping grant with zero
> frequency`). A grant that survives passes lockout, hold/avoid and the
> scan-list gate, binds a `VoiceDevice` (`VoicePool.Bind` → `SetCenterFreq`,
> fresh `CallID`) and publishes `CallStart`. `classifyVoiceKind` maps
> `motorola` / `ltr` / `mpt1327` / non-ProVoice `edacs` to `voiceKindFM`
> and starts `runFMChain`: 81-tap decimating FIR to 48 kHz, optional
> `fm_channel_bandwidth_hz` filter, `demod.FM`, 300 Hz high-pass,
> de-emphasis, 3.4 kHz low-pass, AGC, 8 kHz PCM. No FM-era decoder publishes
> a release; the call ends by hangtime (3.5 s after PCM stops), the 30 s
> watchdog, or the engine moving the device. Rung: composer-pinned by
> `TestComposerRunsFMChainForAnalogTrunking`; trunked analog voice is on-air
> unverified for all four, and unreachable for three.

**Key takeaways**

- **A grant is only as good as its frequency.** The engine drops
  `FrequencyHz` 0 before any other gate, and three of the four FM-era
  pipelines produce exactly that today.
- **The analog chain is the conventional scanner's chain.** `runFMChain`
  is one function serving `fm-conv`, `am-conv` and the analog-trunk
  protocols; the field fixes from issues #1184 and #1090 landed there, so
  trunked analog inherits them.
- **Nothing in the signalling ends an analog call.** With no release message
  and no squelch decision on a trunked tap, the `boundaryTracker`'s
  hangtime, the `call_timeout_ms` watchdog and pool preemption are the only
  ends — and PCM keeps flowing while IQ does.
- **Verified means the composer, not the air.** `status.md`'s "analog FM
  trunking decodes voice through the composer's FM chain" is a statement
  about `classifyVoiceKind` and `runFMChain`, pinned by tests that hand the
  composer a grant with a frequency already filled in.

## Cheat sheet

| Concern | What it does | Where it lives |
|---|---|---|
| Channel → Hz | Motorola: built-in `BandPlan.Frequency(ch)`; EDACS / LTR / MPT: `Resolver.Frequency` — not installed by the factories | `internal/radio/motorola/bandplan.go`, `edacs/bandplan.go`, `ltr/bandplan.go`, `mpt1327/bandplan.go` |
| First gate | `if g.FrequencyHz == 0 { Warn("dropping grant with zero frequency"); return }` | `internal/trunking/engine.go` (`HandleGrant`) |
| Bind | `VoicePool.Bind` → `d.Tuner.SetCenterFreq(g.FrequencyHz)`, `g.CallID = callSeq.Add(1)`, `KindCallStart` | `internal/trunking/voicepool.go`, `engine.go` (`startCall`) |
| Chain selection | `isAnalogTrunk` → `voiceKindFM`; EDACS ProVoice → `voiceKindUnsupported` | `internal/voice/composer/composer.go` (`classifyVoiceKind`) |
| The chain | decimate → channel filter → FM → HPF → de-emphasis → LPF → AGC → PCM | `composer.go` (`runFMChain`, `newFMChannelFilter`) |
| Knobs | `fm_deemphasis`, `fm_audio_highpass_hz` 300, `fm_audio_lowpass_hz` 3400, `fm_channel_bandwidth_hz` 0, `equalizer.enabled` | `config.example.yaml` (`recordings:`) |
| Call end | `boundaryTracker` hangtime (`voice_hangtime_ms` 3500), no-voice 2× window, `call_timeout_ms` 30000 | `composer/boundary.go`, `engine.go` (`runWatchdog`) |
| Pins | `TestComposerRunsFMChainForAnalogTrunking`, `TestComposerBypassesEDACSProVoice`, `TestFMChannelFilterSelectivity`, `TestComposerTailFadeOnCallEnd` | `internal/voice/composer/composer_test.go` |

## In this post

- **Four grants, one resolver gap** — who fills `FrequencyHz`, and who doesn't.
- **The engine's path to a tuner** — gates, bind, retune, preemption.
- **Inside `runFMChain`** — the stages, their order, and the keys that shape them.
- **Ending a call nobody signals** — hangtime, the watchdog, and the noise after the carrier.
- **The recorder's side, and the rung** — what lands on disk, and what has been verified.

## Four grants, one resolver gap

Parts 3 to 6 each ended in the same function shape. Motorola's
`publishGrant` takes a grant OSW whose address is the talkgroup and whose
command is the channel number, EDACS's takes a `GroupVoiceGrant` with an
LCN, LTR's takes an active `Status` with a repeater channel, MPT 1327's a
`GoToChannel`. All four stamp `ChannelNum` and `FrequencyHz` on a
`trunking.Grant`. The difference is where the hertz come from.

Motorola is self-contained. `motorola.BandPlan` ports trunk-recorder's
`get_freq` / `is_chan` tables, `motorola_band_plan` selects `800_standard`
(the default), `800_rebanded`, `800_splinter` or `900`, and
`newMotorolaPipeline` hands the parsed plan to `motorola.New`. The plan is
also the OSW state machine's discriminator — a command value inside the
plan *is* a channel number — so an unresolvable command is not a grant at
all, and `publishGrant` returns without publishing:

```go
// internal/radio/motorola/control.go (shape)
freq, ok := c.plan.Frequency(grantOSW.Command)
if !ok {
    return // not a channel in this plan — nothing to publish
}
```

The other three were written to the same `Resolver` idiom — a
`LinearBandPlan` (`BaseHz + (channel + Offset) × SpacingHz`) or a
`TableBandPlan` map, each with its own unit tests — and each `publishGrant`
does the honest thing when `c.resolver` is nil: it logs nothing, sets
`freq` to 0 and publishes anyway. The gap is upstream. `newEDACSPipeline`,
`newLTRPipeline` and `newMPT1327Pipeline` construct their control channels
with `Bus`, `Log`, `SystemName` and `FrequencyHz` only; the sole `Resolver:`
in `pipelines.go` is DMR Tier III's `tier3.ResolverFromPlan`, and
`config.SystemConfig` carries `motorola_band_plan`, `p25_band_plan`,
`dmr_band_plan` and `nxdn_band_plan` — nothing for EDACS, LTR or MPT 1327.
`TestControlChannelGrantWithoutResolverHasZeroFreq` in the MPT package pins
the zero; nothing pins the wiring, because there is none.

<figure class="lab-figure">
<svg viewBox="0 0 680 230" width="680" height="230" role="img" aria-label="A pipeline from four FM-era decoders to a WAV file. Four boxes on the left, Motorola, EDACS, LTR and MPT 1327, each emit a grant with a channel number. A resolver stage follows: Motorola's is solid and labelled built-in band plan; EDACS, LTR and MPT 1327 have dashed resolver boxes labelled none installed, frequency zero. All four feed the engine's HandleGrant, whose first gate reads drop if FrequencyHz is zero; the three dashed paths stop there. The surviving path continues through lockout, hold and avoid and scan-list gates to VoicePool.Bind and CallStart, then into the composer's classifyVoiceKind and runFMChain, and finally the recorder's WritePCM and a WAV.">
  <text x="8" y="36" fill="currentColor" font-size="9" font-weight="bold">motorola</text>
  <text x="8" y="80" fill="currentColor" font-size="9" font-weight="bold">edacs</text>
  <text x="8" y="124" fill="currentColor" font-size="9" font-weight="bold">ltr</text>
  <text x="8" y="168" fill="currentColor" font-size="9" font-weight="bold">mpt1327</text>
  <rect x="66" y="22" width="90" height="22" fill="none" stroke="currentColor"/>
  <text x="111" y="37" text-anchor="middle" fill="currentColor" font-size="8">grant · ChannelNum</text>
  <rect x="66" y="66" width="90" height="22" fill="none" stroke="currentColor"/>
  <text x="111" y="81" text-anchor="middle" fill="currentColor" font-size="8">grant · LCN</text>
  <rect x="66" y="110" width="90" height="22" fill="none" stroke="currentColor"/>
  <text x="111" y="125" text-anchor="middle" fill="currentColor" font-size="8">grant · Channel</text>
  <rect x="66" y="154" width="90" height="22" fill="none" stroke="currentColor"/>
  <text x="111" y="169" text-anchor="middle" fill="currentColor" font-size="8">grant · Channel</text>
  <rect x="170" y="22" width="110" height="22" fill="none" stroke="var(--accent)"/>
  <text x="225" y="37" text-anchor="middle" fill="var(--accent)" font-size="8">built-in BandPlan → Hz</text>
  <rect x="170" y="66" width="110" height="22" fill="none" stroke="var(--fg-muted)" stroke-dasharray="4 3"/>
  <text x="225" y="81" text-anchor="middle" fill="var(--fg-muted)" font-size="8">Resolver: none → 0 Hz</text>
  <rect x="170" y="110" width="110" height="22" fill="none" stroke="var(--fg-muted)" stroke-dasharray="4 3"/>
  <text x="225" y="125" text-anchor="middle" fill="var(--fg-muted)" font-size="8">Resolver: none → 0 Hz</text>
  <rect x="170" y="154" width="110" height="22" fill="none" stroke="var(--fg-muted)" stroke-dasharray="4 3"/>
  <text x="225" y="169" text-anchor="middle" fill="var(--fg-muted)" font-size="8">Resolver: none → 0 Hz</text>
  <line x1="280" y1="33" x2="300" y2="33" stroke="var(--accent)"/>
  <line x1="280" y1="77" x2="300" y2="77" stroke="var(--fg-muted)" stroke-dasharray="2 2"/>
  <line x1="280" y1="121" x2="300" y2="121" stroke="var(--fg-muted)" stroke-dasharray="2 2"/>
  <line x1="280" y1="165" x2="300" y2="165" stroke="var(--fg-muted)" stroke-dasharray="2 2"/>
  <rect x="300" y="22" width="96" height="154" fill="none" stroke="currentColor"/>
  <text x="348" y="40" text-anchor="middle" fill="currentColor" font-size="8" font-weight="bold">HandleGrant</text>
  <text x="348" y="56" text-anchor="middle" fill="var(--accent)" font-size="8">FrequencyHz == 0 ?</text>
  <text x="348" y="68" text-anchor="middle" fill="var(--accent)" font-size="8">→ drop</text>
  <text x="348" y="96" text-anchor="middle" fill="var(--fg-muted)" font-size="8">lockout</text>
  <text x="348" y="110" text-anchor="middle" fill="var(--fg-muted)" font-size="8">hold / avoid</text>
  <text x="348" y="124" text-anchor="middle" fill="var(--fg-muted)" font-size="8">scan list</text>
  <text x="348" y="150" text-anchor="middle" fill="currentColor" font-size="8">startCall</text>
  <text x="348" y="162" text-anchor="middle" fill="currentColor" font-size="8">VoicePool.Bind</text>
  <line x1="396" y1="33" x2="416" y2="33" stroke="var(--accent)"/>
  <rect x="416" y="22" width="110" height="22" fill="none" stroke="currentColor"/>
  <text x="471" y="37" text-anchor="middle" fill="currentColor" font-size="8">SetCenterFreq · CallStart</text>
  <line x1="526" y1="33" x2="546" y2="33" stroke="var(--accent)"/>
  <rect x="546" y="22" width="126" height="22" fill="none" stroke="currentColor"/>
  <text x="609" y="37" text-anchor="middle" fill="currentColor" font-size="8">classifyVoiceKind → FM</text>
  <line x1="609" y1="44" x2="609" y2="62" stroke="var(--accent)"/>
  <rect x="546" y="62" width="126" height="22" fill="none" stroke="var(--accent)"/>
  <text x="609" y="77" text-anchor="middle" fill="var(--accent)" font-size="8">runFMChain → PCM</text>
  <line x1="609" y1="84" x2="609" y2="102" stroke="var(--accent)"/>
  <rect x="546" y="102" width="126" height="22" fill="none" stroke="currentColor"/>
  <text x="609" y="117" text-anchor="middle" fill="currentColor" font-size="8">recorder.WritePCM → WAV</text>
  <text x="340" y="212" text-anchor="middle" fill="var(--fg-muted)" font-size="8">solid: a path a live grant can take today · dashed: stops at the engine's first gate until a resolver is wired</text>
</svg>
<figcaption>Only the Motorola lane carries hertz into the engine. The other three publish a channel number and a zero, and the engine's first gate ends their journey before the voice pool is consulted.</figcaption>
</figure>

## The engine's path to a tuner

A grant with a frequency enters `Engine.HandleGrant` and meets the gates
the [Trunking Engine]({{ '/blog/series/trunking-engine/' | relative_url }})
series described, in this order: the zero-frequency drop, then the
TETRA-only radio/talkgroup reclassification (gated to `g.Protocol ==
"tetra"`, so it never touches these four), then the talkgroup lookup and
lockout (`grant locked out`, emergency bypasses), then `holdAvoid.gate`, then
the scan-list mode. Then the pool. If the same device already holds this
call the grant is a refresh (`grant already active; refreshed`); if the
device holds the same call on another frequency and its tuner `CanTune`
the new one, `pool.Retune` moves it (`call followed to new frequency`); a
free device gets `startCall`; and with none free, `CanPreempt` decides
whether a lower-priority victim is ended with `EndReasonPreempted` or the
grant is published as `KindGrantUnserved` with `UnfollowedAllBusy`.

`startCall` is where the radio moves:

```go
// internal/trunking/voicepool.go (shape) — Bind
if err := d.Tuner.SetCenterFreq(g.FrequencyHz); err != nil { /* reacquire the handle, retry once */ }
g.CallID = p.callSeq.Add(1) // process-unique; the chain and recorder fence on it
// internal/trunking/engine.go (shape) — startCall
e.bus.Publish(events.Event{Kind: events.KindCallStart, Payload: CallStart{
    Grant: ac.Grant, Talkgroup: tg, DeviceSerial: d.Serial, StartedAt: ac.StartedAt,
}})
e.applyEncryptedPolicy(d.Serial, g, g.Encrypted)
```

The published `CallStart` carries `ac.Grant`, not the caller's copy, because
`Bind` stamped the fresh `CallID`. `applyEncryptedPolicy` honours the
`Encrypted` flag an EDACS or Motorola grant may carry — an EDACS CCW's
status nibble and a SmartNet OSW both flag it — so a `skip_encrypted` policy
applies to analog trunking too, even though there is nothing to decrypt.

For EDACS one more field matters: `ProVoice`. An EDACS `GroupVoiceGrant`
carries it from `CmdProVoiceGrant`, the grant publishes it, and the composer
reads it next. The engine does not care; the recorder does — a ProVoice
grant always forces a `.raw` sidecar, since no in-binary decoder exists for
Aegis / ProVoice.

## Inside `runFMChain`

The composer's `handleStart` computes one `voiceKind` per call. The digital
protocols match by name first; then:

```go
// internal/voice/composer/composer.go (shape) — classifyVoiceKind
isAnalogTrunk := proto == "motorola" || proto == "ltr" || proto == "mpt1327" ||
    (proto == "edacs" && !cs.Grant.ProVoice)
if proto == "" || proto == "fm" || proto == "fm-conv" || proto == "analog" || isAnalogTrunk {
    return voiceKindFM
}
return voiceKindUnsupported // YSF, EDACS ProVoice: "digital protocol not yet decoded; chain bypassed"
```

`TestComposerRunsFMChainForAnalogTrunking` publishes a `CallStart` for each
of `motorola`, `edacs`, `ltr` and `mpt1327` with `FrequencyHz` 851 000 000
and waits for a chain on `VOICE-1`; `TestComposerBypassesEDACSProVoice`
asserts a `ProVoice: true` EDACS grant spawns none. The FM kind launches
`runFMChain(ctx, serial, iqCh, uint32(math.Round(rateHzF)), false, done)` —
the `false` is the AM flag, and the rounded integer rate is fine because, as
the dispatch comment says, analog FM has no symbol clock to drift.

The chain is the one
[Voice Coding Part 9]({{ '/blog/deep-dives/voice-coding-09-the-composer/' | relative_url }})
sketched, and it has grown stages since, every one from a field report on
conventional FM. In order:

1. **Front-end decimation.** `newDecimatingFIR(iqHz, 48_000, c.bw, true)` —
   an 81-tap Kaiser low-pass at `VoiceBandwidthHz` (12 500 by default) that
   convolves only at output positions, decimating a 2.4 MS/s tuner to the
   48 kHz intermediate rate. A wideband virtual tuner already at 48 kHz
   passes through with `decim` 1 but is still filtered (`filterAtUnity`).
2. **Channel filter.** `newFMChannelFilter` builds a second 81-tap complex
   low-pass at 48 kHz, ±half of `fm_channel_bandwidth_hz`, only when the key
   is set. The comment explains why it is a second filter: at 2.4 MS/s an
   81-tap FIR has a ~100 kHz transition band and cannot separate a
   12.5 kHz channel from its neighbour; at 48 kHz the same FIR is 2–3 kHz
   sharp. `TestFMChannelFilterSelectivity` (#1184) pins that it passes
   in-channel audio, attenuates an adjacent-channel tone, and leaves the
   digital chains' `c.bw` untouched.
3. **Optional CMA equalizer** (`recordings.equalizer.enabled`), R² = 1
   because the FM carrier is constant-modulus on air.
4. **`demod.FM`** — the quadrature discriminator.
5. **Audio high-pass**, two cascaded Butterworth biquads at
   `fm_audio_highpass_hz` (300): strips the DC a residual carrier offset
   leaves and the 67–250 Hz CTCSS/DCS tones, and runs *before* de-emphasis
   so the de-emphasis integrator never sees the DC
   (`TestComposerFMChainHighPassRemovesDC`).
6. **De-emphasis** (`fm_deemphasis`: `us` 75 µs default, `eu` 50 µs, `off`).
7. **Audio low-pass** (`fm_audio_lowpass_hz` 3400), telephony band-limit
   and anti-alias for the next step.
8. **Audio AGC**, after the LPF so the envelope follower sees clean audio.
9. **Decimation to PCM** — naive by 6 (48 000 / 8000), or the opt-in
   polyphase `AudioResampler` — then `convertToPCM` and
   `c.sink.WritePCM(serial, pcm)`.

Two details matter for trunked analog specifically. `WritePCM` is used
without the `CallID` fence the digital tap path needs, because an analog
chain keys on a stable physical device serial, torn down on `CallEnd` before
the next `CallStart`. And the squelch gate added for the conventional
scanner in #1090 — freeze the AGC and fade to silence while the scanner
reports the channel closed — asks `squelch.SquelchOpen(serial)` and
**leaves the chain ungated when the answer is `ok = false`**, which is what
every analog-trunk chain gets: there is no scanner-side decision for a
trunked voice tap. `TestComposerFMChainIgnoresSquelchWithoutDecision` pins
that case by name — "any non-conventional analog chain — Motorola/LTR/MPT
1327 voice".

## Ending a call nobody signals

Here the FM-era protocols diverge from everything digital. No `publishGrant`
in `motorola`, `edacs` or `ltr` has a companion release; none of the four
publishes `KindCallRelease`. P25 has a terminator, DMR a terminator with LC,
TETRA a D-RELEASE; SmartNet has an OSW stream that simply stops mentioning
the call, LTR a status word whose F-bit clears, and GopherTrunk's decoders do
not turn either into an event. So an analog call ends only by the
mechanisms every chain shares.

`runFMChain` builds a `boundaryTracker` with `grantTG` 0 — gating disabled,
every frame matches — and calls `bt.onVoice(0)` after every successful PCM
write. The tracker's `run` loop ends the call `hangtime` (`VoiceHangtime`,
`voice_hangtime_ms`, default 3500) after the last such write, or after
`noVoiceStartupFactor` × hangtime (7 s) if no PCM was ever written
(`EndReasonTimeout`), and it `Touch`es the engine only when the last-voice
timestamp has advanced — the #356 fix, so a stalled IQ source cannot keep a
call alive through an unconditional heartbeat. Above that sits the engine's
`runWatchdog`: `call_timeout_ms` (30 000) after `LastHeardAt` stops moving,
`EndReasonTimeout` if nothing was ever heard, `EndReasonNormal` otherwise.

The chain's own comment states the consequence: analog FM "emits PCM
continuously, so in practice the engine's grant lifecycle / watchdog bounds
the call". While IQ flows, PCM flows — discriminator noise after the
carrier drops is still PCM — so `onVoice` keeps firing and neither hangtime
nor the watchdog can elapse. What ends a trunked analog call on a live
system is therefore the engine: a new grant that retunes or rebinds the
device, pool preemption, or a `CallEnd` from the API. Nothing in the tree
quiets an analog-trunk tap on carrier loss; the noise-quieting squelch
CLAUDE.md names for the conventional scanner — the `dmrrx.carrierGate`
variance idea from
[DMR End to End Part 9]({{ '/blog/deep-dives/dmr-end-to-end-09-direct-mode-carrier-gate/' | relative_url }})
— is "not built" there either. Nor does the FM chain ever call
`bt.onTransmissionEnd()`; only the P25 Phase 1 chain does, so
`voice_call_grouping: transmission` has no over boundary to roll on and an
analog call is always one file, as
[Recording, Composition & Streaming Part 3]({{ '/blog/deep-dives/recording-streaming-03-assembling-a-call/' | relative_url }})
explained from the recorder's side.

When the end does come, `handleEnd` cancels the chain and blocks on its
`done` channel; `runFMChain`'s `emitTail` writes a 10 ms linear fade from
the last sample to zero so the cut does not click
(`TestComposerTailFadeOnCallEnd`), and only then does the recorder receive
the drain-complete signal that finalizes the file.

## The recorder's side, and the rung

The recorder opens a session on `CallStart` and, for an analog protocol,
opens no vocoder: `RecorderOptions.VocoderForProtocol`'s comment says it
plainly — protocols not in the map "produce no decoded audio — typically
analog protocols (motorola, edacs, ltr, mpt1327) where the composer's FM
chain feeds WritePCM directly". `WritePCM` locks the session and calls
`s.wav.WriteSamples` on the file `NewAudioFileWriter` opened lazily on the
first write, in the configured `recordings.format` (WAV or FLAC). No `.raw`
sidecar exists for analog, except the ProVoice case above. The rest —
`CallComplete`, the call-log row, streaming — is protocol-blind.

Now the ladder. The composer half is **pinned in CI**:
`TestComposerRunsFMChainForAnalogTrunking` for all four protocol names,
`TestComposerBypassesEDACSProVoice`, and the stage tests that came from
conventional-FM field reports — `TestFMChannelFilterSelectivity` and
`TestComposerFMChainHighPassRemovesDC` (#1184),
`TestComposerFMChainMutesSquelchClosedTail` (#1090),
`TestComposerTailFadeOnCallEnd`. The FM chain itself is in daily use under
the conventional scanner, where those reporters' Kenwood and Radtel
captures are real-air fixtures
([From the Issue Tracker Part 16]({{ '/blog/solution-postmortem/from-the-issue-tracker-16-conventional-fm-broker/' | relative_url }})
tells that path's own story).

The trunked half is **on-air unverified for all four**, and the reasons
differ. Motorola's grants reach the engine with hertz, so its whole path —
OSW decode, plan, bind, chain, WAV — is one `#1143` capture away
([Part 3]({{ '/blog/deep-dives/legacy-family-03-smartnet-air-interface/' | relative_url }})).
EDACS, LTR and MPT 1327 cannot reach the engine at all until a resolver is
wired through their factories and a config key names it; their composer
path is verified only with a grant the test filled in by hand. The
`status.md` sentence — "Analog FM trunking (Motorola Type II, EDACS, LTR,
MPT 1327) decodes voice through the composer's FM chain" — is true of the
composer and silent about the gap in front of it. The series rule applies
unchanged: a green synthetic test is never an on-air pass, and a green
composer test is not a followed call.

### How analog trunking shaped the Go code

- **One classifier, computed once.** `voiceKind` replaced a fan of
  `isFM` / `isDMRVoice` booleans recomputed at three sites; the
  `isAnalogTrunk` expression lives in exactly one place.
- **The AM flag reuses the chain.** `runFMChain(…, am bool, …)` swaps the
  discriminator for `demod.AM` and skips de-emphasis and the CMA stage;
  everything downstream is shared.
- **A second filter at the right rate.** `newFMChannelFilter` runs at
  48 kHz because that is where an 81-tap FIR is sharp; nil when unset keeps
  legacy recordings byte-identical.
- **Resolvers are optional by type, mandatory by physics.** Every
  `publishGrant` tolerates a nil `Resolver`; the engine does not tolerate
  the zero it produces.

## Where this goes next

The FM-era half of the family is done: four control channels, one analog
chain, one resolver gap. The AMBE-era half starts with the protocol that has
the most instrumentation and the least air.
[Part 8]({{ '/blog/deep-dives/legacy-family-08-nxdn-physical-layer/' | relative_url }})
takes NXDN's physical layer — 4FSK at 4800 symbols per second, the
direction-specific FSW, the doubled-bit LICH, the 15-bit scrambler model —
and the 1800 Hz deviation knob the committed `NXDN96 IQ.wav` says is wrong
for at least one transmitter.

## FAQ

**Why does GopherTrunk log "dropping grant with zero frequency" on an EDACS, LTR or MPT 1327 system?**
Because those decoders resolve a channel number to hertz through an optional
`Resolver`, and `newEDACSPipeline`, `newLTRPipeline` and `newMPT1327Pipeline`
install none — there is no `edacs_` / `ltr_` / `mpt1327_` band-plan config
key. `publishGrant` then stamps `FrequencyHz` 0 and `Engine.HandleGrant`
drops the grant as its first check.

**How does GopherTrunk record analog trunked voice?**
`classifyVoiceKind` maps `motorola`, `ltr`, `mpt1327` and non-ProVoice
`edacs` grants to `voiceKindFM`, and `runFMChain` decimates the voice IQ to
48 kHz, FM-demodulates it, applies the 300 Hz high-pass, de-emphasis,
3.4 kHz low-pass and AGC, and writes 8 kHz PCM through `WritePCM`. The
recorder opens no vocoder for these protocols and writes the PCM straight to
the WAV or FLAC.

**What ends an analog trunked call?**
Not the signalling: none of the FM-era decoders publishes a release. The
`boundaryTracker` ends a call 3.5 s (`voice_hangtime_ms`) after PCM stops,
or after 7 s if PCM never started; the engine's watchdog fires at
`call_timeout_ms` (30 s) of silence. Because the FM chain writes PCM as long
as IQ flows, on a live system the engine's grant lifecycle or pool
preemption usually ends the call.

**Does `fm_channel_bandwidth_hz` affect digital voice?**
No. It builds a second complex low-pass at the 48 kHz intermediate rate in
`runFMChain` only, ±half the configured width ahead of the FM discriminator.
The digital chains size their own channel-select filters from
`VoiceBandwidthHz`, and `TestFMChannelFilterSelectivity` pins that the
analog knob leaves them untouched.

**Is analog trunked voice verified on air?**
No, for all four protocols. The composer's FM chain is pinned in CI and
field-proven on conventional FM, but no trunked analog call has been
followed on a live daemon: Motorola waits on the #1143 capture, and EDACS,
LTR and MPT 1327 cannot reach the engine until a band-plan resolver is
wired through their pipeline factories.

## Series navigation

**Part 7 of 14** · ←
[Part 6: MPT 1327 — FFSK Codewords, CWSC Tolerance and BCH(64,48)]({{ '/blog/deep-dives/legacy-family-06-mpt1327/' | relative_url }})
· Next →
[Part 8: NXDN Physical Layer — 4FSK at 4800, FSW, LICH and Scrambling]({{ '/blog/deep-dives/legacy-family-08-nxdn-physical-layer/' | relative_url }})
