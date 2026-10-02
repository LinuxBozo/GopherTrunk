package phase1

import (
	"fmt"

	"github.com/MattCheramie/GopherTrunk/internal/cryptolab/engine/p25crypto"
	"github.com/MattCheramie/GopherTrunk/internal/voice/imbe"
)

// Known-key descramble of P25 Phase 1 voice for every OFB-family link-layer
// algorithm GopherTrunk realises a keystream for — DES-OFB (ALGID 0x81),
// two-/three-key TDES (0x83 / 0x86), AES-128 (0x85), AES-256 (0x84 / 0x89)
// — alongside the RC4-based ADP (0xAA) adp.go pins against the #1187
// capture.
//
// The frame mapping is ONE layout for all of them, and it is the ADP one:
// a superframe's keystream has a leading discard the cipher dictates (RC4's
// 256-byte warm-up, one 8-byte DES block, one 16-byte AES block), then an
// 11-byte skip, then LDU1's nine 11-byte IMBE frames contiguously with a
// 2-byte Low Speed Data gap before u8, then LDU2's nine from +101. The
// reference for the non-RC4 members is OP25 (op25_crypt_des.cc /
// op25_crypt_aes.cc: `offset = 8` / `16`, `+101` for LDU2, `+ 11 + 11·i`,
// `+2` at i ≥ 8) — proven on air there; GopherTrunk has NO capture of its own
// for DES/TDES/AES yet, so this file is reference-pinned, not
// capture-pinned (#764/#771: treat it as the layout to A/B a capture
// against, see docs/dmr-encryption.md).
//
// The IV each algorithm seeds OFB with differs:
//   - DES / TDES: the first 8 octets of the 72-bit Message Indicator.
//   - AES: the 64-bit MI expanded to 128 bits by the TIA-102.AAAD LFSR —
//     the first 64 IV bits are the MI itself and the second 64 are the LFSR
//     state after 64 clocks, i.e. IV = MI ‖ AdvanceMI(MI) (OP25
//     expand_mi_to_128 shifts the MI out bit-for-bit while it clocks).
//
// The MI schedule (ES names the NEXT superframe's MI, successive MIs follow
// the LFSR, see AdvanceMI / RewindMI) is the same for every algorithm: it
// is a property of the Encryption Sync, not the cipher.

// voiceLayout is one algorithm's keystream layout: how many leading bytes
// the cipher discards before the common 11-byte skip.
type voiceLayout struct {
	discard int
}

// voiceFrameBaseSkip is the keystream the superframe consumes between the
// cipher's discard and LDU1's u0 (OP25's `+ 11`).
const voiceFrameBaseSkip = 11

// voiceLDU2Offset is how far LDU2's u0 sits past LDU1's (9·11 + 2 LSD).
const voiceLDU2Offset = 101

// voiceLSDSkip is the keystream the Low Speed Data word consumes between
// u7 and u8.
const voiceLSDSkip = 2

// voiceLayoutFor returns the layout for algID, or ok=false when
// GopherTrunk does not descramble that algorithm in-process.
func voiceLayoutFor(algID uint8) (voiceLayout, bool) {
	switch algID {
	case p25crypto.AlgADP:
		return voiceLayout{discard: adpKeystreamDrop}, true
	case p25crypto.AlgDESOFB, p25crypto.AlgTDES2, p25crypto.AlgTDES:
		return voiceLayout{discard: 8}, true
	case p25crypto.AlgAES128, p25crypto.AlgAES256, p25crypto.AlgAES256OFB:
		return voiceLayout{discard: 16}, true
	}
	return voiceLayout{}, false
}

// VoiceDecryptSupported reports whether the Phase 1 voice path can
// descramble algID in-process given an operator key.
func VoiceDecryptSupported(algID uint8) bool {
	_, ok := voiceLayoutFor(algID)
	return ok
}

// VoiceKeyBytes returns the key length algID needs (5 for ADP, 8 DES, 16
// TDES-2 / AES-128, 24 TDES, 32 AES-256), or 0 when unsupported.
func VoiceKeyBytes(algID uint8) int {
	if !VoiceDecryptSupported(algID) {
		return 0
	}
	return p25crypto.KeySize(algID)
}

// VoiceKeystreamDiscard returns the leading keystream bytes algID discards
// (256 / 8 / 16), or 0 when unsupported.
func VoiceKeystreamDiscard(algID uint8) int {
	l, _ := voiceLayoutFor(algID)
	return l.discard
}

// VoiceSuperframeKeystreamBytes is the keystream one superframe consumes
// after the discard — the length VoiceSuperframeKeystream returns. It is
// the same for every algorithm (11 + 2·(9·11 + 2) = 213).
const VoiceSuperframeKeystreamBytes = voiceFrameBaseSkip + 2*(LDUVoiceSubframeCount*imbe.FrameBytes+voiceLSDSkip)

// VoiceFrameOffset returns the ABSOLUTE keystream offset (discard counted,
// as OP25 does) of voice subframe `subframe` of an LDU of type duid under
// algID. ok is false for an unsupported algorithm, a non-voice DUID or an
// out-of-range subframe.
func VoiceFrameOffset(algID uint8, duid DUID, subframe int) (off int, ok bool) {
	l, ok := voiceLayoutFor(algID)
	if !ok || subframe < 0 || subframe >= LDUVoiceSubframeCount {
		return 0, false
	}
	off = l.discard + voiceFrameBaseSkip
	switch duid {
	case DUIDLogicalLink1:
	case DUIDLogicalLink2:
		off += voiceLDU2Offset
	default:
		return 0, false
	}
	off += imbe.FrameBytes * subframe
	if subframe == LDUVoiceSubframeCount-1 {
		off += voiceLSDSkip
	}
	return off, true
}

// VoiceSuperframeKeystream returns the keystream one superframe (LDU1 +
// LDU2) scrambled under mi consumes for algID, the cipher's discard already
// applied: index it with VoiceFrameOffset(…) − VoiceKeystreamDiscard(algID),
// or hand it to DescrambleVoiceFrames.
func VoiceSuperframeKeystream(algID uint8, key []byte, mi [9]byte) ([]byte, error) {
	if !VoiceDecryptSupported(algID) {
		return nil, fmt.Errorf("p25/phase1: algorithm 0x%02X is not descrambled in-process", algID)
	}
	if want := p25crypto.KeySize(algID); len(key) != want {
		return nil, fmt.Errorf("p25/phase1: %s key must be %d bytes, got %d", p25crypto.AlgName(algID), want, len(key))
	}
	// p25crypto derives the IV from the MI the way the reference decoders do
	// and already applies RC4's 256-byte warm-up for ADP; the OFB ciphers
	// come back from byte 0, so their first block (the P25 discard round)
	// is dropped here.
	l, _ := voiceLayoutFor(algID)
	if algID == p25crypto.AlgADP {
		return p25crypto.Keystream(algID, key, mi[:ADPMIBytes], VoiceSuperframeKeystreamBytes)
	}
	ks, err := p25crypto.Keystream(algID, key, mi[:ADPMIBytes], l.discard+VoiceSuperframeKeystreamBytes)
	if err != nil {
		return nil, err
	}
	return ks[l.discard:], nil
}

// DescrambleVoiceFrames XORs the nine FEC-decoded 11-byte IMBE frames of an
// LDU of type duid, in place, with their slots of ks (as returned by
// VoiceSuperframeKeystream for algID and the superframe's MI). A nil frame
// — one whose FEC failed — is skipped but keeps its keystream slot, so the
// rest stay aligned. Returns the number of frames descrambled.
func DescrambleVoiceFrames(algID uint8, ks []byte, duid DUID, frames *[LDUVoiceSubframeCount][]byte) (int, error) {
	l, ok := voiceLayoutFor(algID)
	if !ok {
		return 0, fmt.Errorf("p25/phase1: algorithm 0x%02X is not descrambled in-process", algID)
	}
	if len(ks) < VoiceSuperframeKeystreamBytes {
		return 0, fmt.Errorf("p25/phase1: %s keystream is %d bytes, need %d", p25crypto.AlgName(algID), len(ks), VoiceSuperframeKeystreamBytes)
	}
	if duid != DUIDLogicalLink1 && duid != DUIDLogicalLink2 {
		return 0, fmt.Errorf("p25/phase1: descramble of a %v (not a voice LDU)", duid)
	}
	n := 0
	for i := range frames {
		f := frames[i]
		if f == nil {
			continue
		}
		if len(f) != imbe.FrameBytes {
			return n, fmt.Errorf("p25/phase1: voice subframe %d is %d bytes, want %d", i, len(f), imbe.FrameBytes)
		}
		off, _ := VoiceFrameOffset(algID, duid, i)
		off -= l.discard
		for j := range f {
			f[j] ^= ks[off+j]
		}
		n++
	}
	return n, nil
}
