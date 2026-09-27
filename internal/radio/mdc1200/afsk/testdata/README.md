# MDC1200 reference-encoder fixture

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

A real-air capture from a Motorola radio is still wanted (issue #1220):
`gophertrunk capture` IQ (or an SDR# baseband WAV) of a keyup with a known
unit ID, replayed with `TestMDC1200Replay` (`GT_MDC1200_IQ=…`).
