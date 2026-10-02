// Package p25crypto generates the keystreams P25's link-layer encryption
// algorithms produce from a key and a Message Indicator (IV). It exists so the
// assessment harness can actually *attempt decryption* — generate the real
// keystream for a candidate/known/weak key and XOR it onto captured ciphertext
// — rather than only detecting structural weaknesses.
//
// The constructions follow the common open P25 implementations (OP25,
// DSD-FME):
//
//   - ADP (RC4, ALGID 0xAA): RC4 keyed with the 5-octet key followed by the
//     first 8 octets of the MI, discarding the first 256 keystream bytes.
//   - DES-OFB (0x81): single-DES in OFB, IV = first 8 octets of the MI.
//   - AES-128/256 (0x85 / 0x84 / 0x89): AES in OFB, IV = the 64-bit MI
//     expanded to 128 bits by the TIA-102.AAAD LFSR (ExpandMI): the MI
//     itself followed by the LFSR state after 64 clocks — OP25
//     expand_mi_to_128 / DSD-FME LFSR128, the construction proven on air by
//     both.
//
// These are the byte-stream keystreams; mapping them onto a specific protocol
// payload (e.g. the exact IMBE voice-bit positions) is the caller's concern.
// The package performs no key recovery — it only realises a keystream for a
// key the caller already has or is testing.
package p25crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/des"
	"fmt"

	"github.com/MattCheramie/GopherTrunk/internal/crypto/rc4"
)

// P25 algorithm identifiers (TIA-102.AACE-A). Mirrors
// internal/radio/p25/algorithm.go; duplicated here to keep the engine
// dependency-light.
const (
	AlgDESOFB    = 0x81
	AlgTDES2     = 0x83 // two-key Triple-DES (K1,K2 → K1K2K1)
	AlgAES256    = 0x84
	AlgAES128    = 0x85
	AlgTDES      = 0x86 // three-key Triple-DES
	AlgAES256OFB = 0x89
	AlgADP       = 0xAA
)

// adpDiscard is the number of leading RC4 keystream bytes P25 ADP throws away
// before encrypting (the OP25/DSD-FME convention).
const adpDiscard = 256

// KeySize returns the key length in bytes p25crypto expects for algid, or 0
// when the algorithm is not supported here.
func KeySize(algid uint8) int {
	switch algid {
	case AlgADP:
		return 5
	case AlgDESOFB:
		return 8
	case AlgTDES2:
		return 16
	case AlgTDES:
		return 24
	case AlgAES128:
		return 16
	case AlgAES256, AlgAES256OFB:
		return 32
	}
	return 0
}

// Supported reports whether Keystream can realise this algorithm.
func Supported(algid uint8) bool { return KeySize(algid) != 0 }

// AlgName returns a short human label for algid.
func AlgName(algid uint8) string {
	switch algid {
	case AlgDESOFB:
		return "DES-OFB"
	case AlgTDES2:
		return "TDES-2"
	case AlgTDES:
		return "TDES"
	case AlgAES256:
		return "AES-256"
	case AlgAES128:
		return "AES-128"
	case AlgAES256OFB:
		return "AES-256-OFB"
	case AlgADP:
		return "ADP/RC4"
	}
	return fmt.Sprintf("alg-0x%02X", algid)
}

// Keystream returns n bytes of keystream for the given algorithm, key, and
// message indicator (IV). It validates the key length against KeySize.
func Keystream(algid uint8, key, mi []byte, n int) ([]byte, error) {
	if n < 0 {
		return nil, fmt.Errorf("p25crypto: negative length")
	}
	if want := KeySize(algid); want == 0 {
		return nil, fmt.Errorf("p25crypto: unsupported algorithm 0x%02X", algid)
	} else if len(key) != want {
		return nil, fmt.Errorf("p25crypto: %s needs a %d-byte key, got %d", AlgName(algid), want, len(key))
	}
	switch algid {
	case AlgADP:
		return adpKeystream(key, mi, n)
	case AlgDESOFB:
		return ofbKeystream(newDES, key, miIV(mi, des.BlockSize), n)
	case AlgTDES2:
		// Two-key 3DES: expand K1‖K2 into the 24-byte K1‖K2‖K1 form.
		k := make([]byte, 24)
		copy(k[0:8], key[0:8])
		copy(k[8:16], key[8:16])
		copy(k[16:24], key[0:8])
		return ofbKeystream(newTDES, k, miIV(mi, des.BlockSize), n)
	case AlgTDES:
		return ofbKeystream(newTDES, key, miIV(mi, des.BlockSize), n)
	case AlgAES128, AlgAES256, AlgAES256OFB:
		return ofbKeystream(newAES, key, ExpandMI(miBytes(mi, 8)), n)
	}
	return nil, fmt.Errorf("p25crypto: unsupported algorithm 0x%02X", algid)
}

func adpKeystream(key, mi []byte, n int) ([]byte, error) {
	rcKey := make([]byte, 0, 13)
	rcKey = append(rcKey, key...) // 5 bytes
	rcKey = append(rcKey, miBytes(mi, 8)...)
	c, err := rc4.NewCipher(rcKey)
	if err != nil {
		return nil, fmt.Errorf("p25crypto: adp: %w", err)
	}
	c.KeyStream(adpDiscard) // discard the warm-up bytes
	return c.KeyStream(n), nil
}

func newDES(key []byte) (cipher.Block, error)  { return des.NewCipher(key) }
func newTDES(key []byte) (cipher.Block, error) { return des.NewTripleDESCipher(key) }
func newAES(key []byte) (cipher.Block, error)  { return aes.NewCipher(key) }

func ofbKeystream(newBlock func([]byte) (cipher.Block, error), key, iv []byte, n int) ([]byte, error) {
	block, err := newBlock(key)
	if err != nil {
		return nil, fmt.Errorf("p25crypto: %w", err)
	}
	stream := cipher.NewOFB(block, iv)
	out := make([]byte, n)
	stream.XORKeyStream(out, out) // XOR against zeros = raw keystream
	return out, nil
}

// ExpandMI expands the 64-bit Message Indicator (its first 8 octets; a
// shorter slice is zero-padded) into the 128-bit AES IV TIA-102.AAAD
// prescribes: the MI's 64 bits are clocked through the LFSR
// x^64 + x^62 + x^46 + x^38 + x^27 + x^15 + 1 sixty-four times, the bits
// shifted OUT form IV[0:8] (which is the MI itself, MSB first) and the
// register left behind forms IV[8:16]. This is OP25's expand_mi_to_128
// (op25_crypt_algs.cc) and DSD-FME's LFSR128, both on-air proven; the
// second half is the same step the ES uses to announce the next
// superframe's MI, so IV = MI ‖ next-MI.
func ExpandMI(mi []byte) []byte {
	var v uint64
	for i := 0; i < 8; i++ {
		var b byte
		if i < len(mi) {
			b = mi[i]
		}
		v = v<<8 | uint64(b)
	}
	var out uint64
	for i := 0; i < 64; i++ {
		ov := (v >> 63) & 1
		fb := ((v >> 63) ^ (v >> 61) ^ (v >> 45) ^ (v >> 37) ^ (v >> 26) ^ (v >> 14)) & 1
		v = v<<1 | fb
		out = out<<1 | ov
	}
	iv := make([]byte, 16)
	for i := 7; i >= 0; i-- {
		iv[i] = byte(out)
		out >>= 8
	}
	for i := 15; i >= 8; i-- {
		iv[i] = byte(v)
		v >>= 8
	}
	return iv
}

// miIV left-justifies the MI into a size-byte IV block (zero-padded /
// truncated): for DES the 64-bit IV is the MI's first 8 octets exactly.
func miIV(mi []byte, size int) []byte {
	iv := make([]byte, size)
	copy(iv, miBytes(mi, size))
	return iv
}

// miBytes returns up to n bytes of the MI.
func miBytes(mi []byte, n int) []byte {
	if len(mi) >= n {
		return mi[:n]
	}
	return mi
}

// DefaultKeys returns a small dictionary of weak / default / test keys to try
// for algid: the all-zero key, the all-FF key, and a simple incrementing
// pattern. These are the keys a misconfigured or factory-default radio is most
// likely to carry. Returns nil for unsupported algorithms.
func DefaultKeys(algid uint8) [][]byte {
	sz := KeySize(algid)
	if sz == 0 {
		return nil
	}
	zero := make([]byte, sz)
	ff := make([]byte, sz)
	inc := make([]byte, sz)
	for i := range ff {
		ff[i] = 0xFF
		inc[i] = byte(i + 1)
	}
	return [][]byte{zero, ff, inc}
}
