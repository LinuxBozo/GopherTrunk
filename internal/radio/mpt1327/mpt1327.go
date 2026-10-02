// Package mpt1327 decodes MPT 1327 trunked control-channel signaling
// — the UK / Commonwealth utility trunking system standardised by the
// UK Department of Trade and Industry's Code of Practice MPT 1327
// (1988). Still in deployment for taxis, transport, and government
// utility fleets across Europe, Australia, New Zealand, and parts of
// Asia, Africa, and South America.
//
// Wire format: continuous 1200 baud FFSK on the control channel,
// 64-bit codewords back-to-back. Each codeword is 38 information
// bits + a 26-bit BCH(63,38)-derived check (folded into a 64-bit
// transmission unit). Address codewords carry trunked signalling;
// data codewords (signalled by the leading codeword-type bit)
// transport short messages.
//
// Files:
//
//	codeword.go  Codeword = {Type, Prefix, Ident, Function} packed
//	             into the upper 38 bits of a 64-bit transmission
//	             unit. Round-trip assemble / parse helpers and bit
//	             accessors.
//	opcodes.go   CodewordKind enum + per-kind payload accessors
//	             — Aloha (ALH) idle, Address Hang Up Yes (AHY)
//	             paging, Aloha Hang Up Yes Channel (AHYC) broadcast,
//	             Go To Channel (GTC) voice grant, Disconnect
//	             Unique-to-Line (DUL), Acknowledge (ACK).
//	bandplan.go  Channel number → Hz resolver. MPT 1327 channel
//	             numbering is system-specific; both linear and
//	             table strategies are exposed.
//	control.go   Control-channel state machine that ingests
//	             codewords, locks on the first valid Aloha (ALH)
//	             or AHYC broadcast, and republishes GTC grants as
//	             events.KindGrant with Protocol="mpt1327".
//
// Now wired (the deferrals this comment once listed):
//
//   - The 1200-baud FFSK demodulator (1200 / 1800 Hz mark/space) lives
//     in the receiver sub-package and feeds ControlChannel.Process
//     through the ccdecoder MPT 1327 pipeline.
//   - BCH(63,38) decode of the 64-bit on-air codeword runs in process.go
//     under SetBCHMode(BCHOn), the production default; BCHOff keeps the
//     pre-corrected 48-bit path for callers that FEC upstream.
//   - Codeword-sync (CCSC) alignment and slot framing are handled by the
//     bit-stream adapter in process.go, which searches the stream for
//     the system codeword and slices codewords on that boundary.
package mpt1327
