---
layout: page
title: ACARS (VHF air band)
description: Decoded ACARS aircraft data link off conventional-scanner AM channels — DSP front end, block check and repair, SQLite log, REST endpoint, web panel
nav_group: Reference
---

# ACARS (VHF air band)

GopherTrunk decodes **ACARS** — the Aircraft Communications Addressing and
Reporting System, the plain-old VHF data link aircraft and ground stations
use on 131.550, 131.525, 130.025, 129.125, 131.125 MHz and friends. It runs
off a **`scanner.conventional` AM channel**, the same way MDC1200 and
FleetSync hang off an FM channel: no SDR has to be dedicated to it.

Added in response to [#1231](https://github.com/MattCheramie/GopherTrunk/issues/1231).

## Configuration

```yaml
storage:
  path: ./gophertrunk.db          # the decoded blocks live in the SQLite log

scanner:
  conventional:
    - label: "ACARS primary"
      frequency_hz: 131550000
      mode: am                    # required: ACARS rides an AM carrier
      decoders: [acars]
```

`acars` is accepted only on a `mode: am` channel. The AM channel's
carrier-to-noise squelch (`squelch_cn_db`, default 12 dB) opens the channel;
while a block is being framed the scanner holds the channel (up to 1 s — a
full ACARS block is ~0.9 s) so a hop cannot cut it off.

Decoded blocks land in:

- the `acars_log` SQLite table (swept by `retention.log_days`),
- `GET /api/v1/acars/messages?limit=N` (503 without `storage.path`),
- the **ACARS** panel of the web console (`web.tabs.acars: false` hides it),
- the `acars.message` event on the SSE / WebSocket stream.

Each row carries the aircraft registration, the flight id and message number
(downlinks), the label, block id, the text, the channel it came from, and
whether the block check validated — `fixed N` when N bits had to be
repaired first.

## Air interface

| | |
| --- | --- |
| Modulation | 2400 bit/s MSK on audio tones 1200 / 2400 Hz, amplitude-modulated onto the carrier |
| Line code | 2400 Hz when a data bit equals the previous one, 1200 Hz when it changes |
| Characters | 7-bit ASCII, LSB first, odd parity in bit 7 |
| Block | pre-key (≥16 × 0xFF) · `+` `*` SYN SYN · SOH · mode · address (7) · ack · label (2) · block id · STX · text (≤220) · ETX/ETB · BCS (2) · DEL |
| Block check | CRC-16/KERMIT (reflected 0x1021, init 0) over mode … suffix including parity bits; the two BCS bytes, low first, make it zero |
| Downlink text | message number (4) + flight id (6) + body |

The layout, parity sense, bit order, block check and line code were read from
[acarsdec](https://github.com/f00b4r0/acarsdec) (GPL — read for protocol
facts only, nothing ported) and then confirmed in both directions: acarsdec
decodes GopherTrunk's synthesised audio (and of the four possible
tone/line-code mappings it decodes only the one above), and GopherTrunk
decodes the seven real-air messages in acarsdec's own test recording to the
same fields acarsdec prints. Literal on-air blocks from that recording pin
the parser (`internal/radio/acars/acars_test.go`).

## Pipeline

```
channel IQ (~48 kHz, from the scanner's data front end)
  → AM envelope |x|                     (no carrier recovery needed:
                                          ppm / tuning offset costs nothing)
  → resample to 19.2 kHz (8 samples/bit)
  → coherent MSK demodulator:
      mix by the 1800 Hz centre → low-pass
      timing: Gardner loop on the one-bit phase difference
      data:   absolute phase at each bit boundary (BPSK after de-rotation)
  → framer: 48-bit sync hunt (either polarity) → characters → suffix → BCS
  → block check, with repair of up to three single-bit character errors
    (located by parity) or one adjacent bit pair, only when the repair is
    unique and the result is printable ACARS
```

Why coherent: the tones encode whether a bit *changed*, so a receiver that
decides tones has to integrate them, and one wrong tone then inverts the
rest of the block. On acarsdec's recording a tone-deciding front end decoded
5 of 7 messages; the coherent detector decodes all 7, and with white noise
added it decodes as many or more messages than acarsdec at every level
tried.

## Verifying a capture

`TestACARSReplay` replays a recording through the production front end and
prints every block:

```
# AM-demodulated audio (rtl_fm -M am, any rate, any channel count)
GT_ACARS_AUDIO=capture.wav go test ./cmd/gophertrunk -run TestACARSReplay -v

# raw IQ (wav/flac container, or headerless f32 / cs16)
GT_ACARS_IQ=capture.cs16 GT_ACARS_FORMAT=cs16 GT_ACARS_RATE=2400000 \
  GT_ACARS_TUNE_HZ=0 go test ./cmd/gophertrunk -run TestACARSReplay -v
```

`GT_ACARS_EXPECT=N` turns the report into an assertion of at least N
CRC-valid blocks.

## Known limits

- An ACARS block on a scan-list channel also opens an ordinary AM "call"
  (the channel's carrier is up), so short data-burst recordings appear in
  the call history beside the decoded messages.
- Multi-block messages (ETB) are logged block by block; they are not
  reassembled.
- Labels are shown raw; no label-specific payload decoding (positions,
  OOOI times, …) is done.
- VDL Mode 2 (the 31.5 kbit/s D8PSK successor) is a different air interface
  and is not decoded.
