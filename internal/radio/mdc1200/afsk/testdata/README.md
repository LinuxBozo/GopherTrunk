# MDC1200 fixtures

## Real-air slice (the on-air pin)

`mdc1200_unit1777_pttid_end_48k.cs16` is a channelized slice of the on-air
capture @v2maldo posted for issue #1220: a Motorola radio programmed as
**unit 0x1777** keying on **447.100 MHz**, recorded by the GopherTrunk
scanner's own voice recorder (`…_447100000_2400000_voice.cs16`, 2.4 MS/s
cs16, 2.18 s; the scanner opens the recording on squelch, so the keyup PTT
ID is cut off and the clip carries the **end-of-transmission PTT ID**, op
0x01 / arg 0x00, at t ≈ 1.3 s).

| File | Source | Content |
| --- | --- | --- |
| `mdc1200_unit1777_pttid_end_48k.cs16` | scanner voice recording, 2.4 MS/s cs16, carrier at −1758 Hz | 0.7 s from t = 0.8 s: one PTT ID (end) burst, unit 0x1777 |

Produced by the production `ccdecoder.Downconverter` (tune to −1758 Hz,
48 kHz output), peak-normalised to 0.9 and written as headerless
interleaved int16 I/Q (`cs16`, little-endian). The original capture is
ADC-clipped (RMS −0.9 dBFS, both rails; the handheld was in the same
room) and decodes anyway. Replay the slice with:

```
GT_MDC1200_IQ=$PWD/internal/radio/mdc1200/afsk/testdata/mdc1200_unit1777_pttid_end_48k.cs16 \
GT_MDC1200_FORMAT=cs16 GT_MDC1200_RATE=48000 GT_MDC1200_UNIT=1777 \
  go test ./cmd/gophertrunk -run 'TestMDC1200Replay$' -v
```

`realair_test.go` decodes it through `afsk.Receiver` and asserts the unit
ID. The full 20 MB capture is linked from the issue; the same reporter's
live run decoded eleven PTT ID bursts (start and end) on narrow and wide
FM through the scanner-channel path.

## Reference-encoder audio

`mdc1200_ref_01_80_1234_48k.s16` is discriminator audio produced by the
reference MDC1200 modem (Matthew Kaufman's `mdc-encode-decode`,
`mdc_encode.c`, built for float samples at 48 kHz) for one single packet:
**op 0x01, arg 0x80, unit ID 0x1234** (a PTT ID), with the encoder's default
7-byte leader, no extra preamble and its default 68 % amplitude, followed by
0.2 s of silence. Mono, 16-bit signed little-endian PCM, 48 000 Hz,
17 921 samples (0.37 s). The reference decoder decodes this file back to the
same packet.

It is data produced by the reference program, not its code; nothing from
the (GPL) reference is ported into this repository. It exists because a
synthetic round-trip through our own encoder cannot catch a shared wrong
line code (#764/#771) — and for the life of the decoder before #1220 our
own encoder and receiver agreed on a plain-NRZ line code that no radio
uses. `airformat_test.go` decodes it through `Receiver.ProcessAudio`.
