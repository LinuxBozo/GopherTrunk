package phase1

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/des"
	"testing"

	"github.com/MattCheramie/GopherTrunk/internal/cryptolab/engine/p25crypto"
)

// TestVoiceFrameOffsetsMatchOP25 pins every OFB-family layout against the
// literal offsets OP25 computes (op25_crypt_des.cc / op25_crypt_aes.cc:
// `offset = 8` or `16`, `+101` for LDU2, `+ 11 + 11·i`, `+2` at i = 8) —
// the only kind of test that catches constant drift. ADP's 267/368 are
// re-pinned here so the shared layout cannot move it.
func TestVoiceFrameOffsetsMatchOP25(t *testing.T) {
	cases := []struct {
		alg   uint8
		ldu1  []int
		ldu2  []int
		drop  int
		kbyte int
	}{
		{p25crypto.AlgADP,
			[]int{267, 278, 289, 300, 311, 322, 333, 344, 357},
			[]int{368, 379, 390, 401, 412, 423, 434, 445, 458}, 256, 5},
		{p25crypto.AlgDESOFB,
			[]int{19, 30, 41, 52, 63, 74, 85, 96, 109},
			[]int{120, 131, 142, 153, 164, 175, 186, 197, 210}, 8, 8},
		{p25crypto.AlgTDES2,
			[]int{19, 30, 41, 52, 63, 74, 85, 96, 109},
			[]int{120, 131, 142, 153, 164, 175, 186, 197, 210}, 8, 16},
		{p25crypto.AlgTDES,
			[]int{19, 30, 41, 52, 63, 74, 85, 96, 109},
			[]int{120, 131, 142, 153, 164, 175, 186, 197, 210}, 8, 24},
		{p25crypto.AlgAES256,
			[]int{27, 38, 49, 60, 71, 82, 93, 104, 117},
			[]int{128, 139, 150, 161, 172, 183, 194, 205, 218}, 16, 32},
		{p25crypto.AlgAES128,
			[]int{27, 38, 49, 60, 71, 82, 93, 104, 117},
			[]int{128, 139, 150, 161, 172, 183, 194, 205, 218}, 16, 16},
		{p25crypto.AlgAES256OFB,
			[]int{27, 38, 49, 60, 71, 82, 93, 104, 117},
			[]int{128, 139, 150, 161, 172, 183, 194, 205, 218}, 16, 32},
	}
	for _, c := range cases {
		if !VoiceDecryptSupported(c.alg) {
			t.Errorf("alg 0x%02X not supported", c.alg)
			continue
		}
		if got := VoiceKeystreamDiscard(c.alg); got != c.drop {
			t.Errorf("alg 0x%02X discard = %d, want %d", c.alg, got, c.drop)
		}
		if got := VoiceKeyBytes(c.alg); got != c.kbyte {
			t.Errorf("alg 0x%02X key bytes = %d, want %d", c.alg, got, c.kbyte)
		}
		for i := 0; i < LDUVoiceSubframeCount; i++ {
			if got, ok := VoiceFrameOffset(c.alg, DUIDLogicalLink1, i); !ok || got != c.ldu1[i] {
				t.Errorf("alg 0x%02X LDU1 u%d offset = %d,%v want %d", c.alg, i, got, ok, c.ldu1[i])
			}
			if got, ok := VoiceFrameOffset(c.alg, DUIDLogicalLink2, i); !ok || got != c.ldu2[i] {
				t.Errorf("alg 0x%02X LDU2 u%d offset = %d,%v want %d", c.alg, i, got, ok, c.ldu2[i])
			}
		}
		// The last LDU2 frame ends exactly at discard + the per-superframe span.
		if last, _ := VoiceFrameOffset(c.alg, DUIDLogicalLink2, 8); last+11 != c.drop+VoiceSuperframeKeystreamBytes {
			t.Errorf("alg 0x%02X superframe span = %d, want %d", c.alg, last+11-c.drop, VoiceSuperframeKeystreamBytes)
		}
	}
	for _, bad := range []uint8{0x80, 0x9F, 0x00, 0xFF} {
		if VoiceDecryptSupported(bad) {
			t.Errorf("alg 0x%02X reported supported", bad)
		}
		if _, ok := VoiceFrameOffset(bad, DUIDLogicalLink1, 0); ok {
			t.Errorf("alg 0x%02X offset accepted", bad)
		}
	}
	if _, ok := VoiceFrameOffset(p25crypto.AlgDESOFB, DUIDTerminator, 0); ok {
		t.Error("TDU accepted")
	}
}

// TestVoiceSuperframeKeystreamIsOFBAfterDiscard recomputes the DES-OFB and
// AES-256 keystreams from Go's own block ciphers with the OP25 IV rules
// (DES: IV = MI[0:8]; AES: IV = expand_mi_to_128 = MI ‖ LFSR64(MI)) and
// checks VoiceSuperframeKeystream returns exactly the bytes AFTER the first
// block — so a frame at absolute offset `off` reads ks[off − discard].
func TestVoiceSuperframeKeystreamIsOFBAfterDiscard(t *testing.T) {
	mi := [9]byte{0x17, 0xCE, 0xEC, 0x55, 0x31, 0x0A, 0x74, 0x75, 0x00}
	ofb := func(block cipher.Block, iv []byte, n int) []byte {
		out := make([]byte, n)
		cipher.NewOFB(block, iv).XORKeyStream(out, out)
		return out
	}
	// DES-OFB
	dkey := []byte{1, 2, 3, 4, 5, 6, 7, 8}
	db, _ := des.NewCipher(dkey)
	full := ofb(db, mi[:8], 8+VoiceSuperframeKeystreamBytes)
	got, err := VoiceSuperframeKeystream(p25crypto.AlgDESOFB, dkey, mi)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, full[8:]) {
		t.Fatal("DES-OFB keystream is not the OFB stream after one discarded block")
	}
	// AES-256 with the LFSR IV expansion: the second IV half is the MI
	// advanced 64 LFSR steps — the same step AdvanceMI performs.
	akey := make([]byte, 32)
	for i := range akey {
		akey[i] = byte(i * 7)
	}
	next := AdvanceMI(mi)
	iv := append(append([]byte{}, mi[:8]...), next[:8]...)
	ab, _ := aes.NewCipher(akey)
	full = ofb(ab, iv, 16+VoiceSuperframeKeystreamBytes)
	got, err = VoiceSuperframeKeystream(p25crypto.AlgAES256, akey, mi)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, full[16:]) {
		t.Fatal("AES-256 keystream is not the OFB stream (IV = MI ‖ AdvanceMI) after one discarded block")
	}
	if !bytes.Equal(p25crypto.ExpandMI(mi[:8]), iv) {
		t.Fatalf("ExpandMI = %x, want %x", p25crypto.ExpandMI(mi[:8]), iv)
	}
	// Wrong key length is refused per algorithm.
	if _, err := VoiceSuperframeKeystream(p25crypto.AlgAES256, akey[:16], mi); err == nil {
		t.Error("AES-256 accepted a 16-byte key")
	}
	if _, err := VoiceSuperframeKeystream(p25crypto.AlgDESOFB, akey[:16], mi); err == nil {
		t.Error("DES accepted a 16-byte key")
	}
}

// TestDescrambleVoiceFramesRoundTrip: scrambling then descrambling with the
// same algorithm/key/MI is the identity for every supported algorithm, a
// different algorithm's keystream is not, and nil (FEC-failed) frames keep
// their slot.
func TestDescrambleVoiceFramesRoundTrip(t *testing.T) {
	mi := [9]byte{9, 8, 7, 6, 5, 4, 3, 2, 1}
	for _, alg := range []uint8{p25crypto.AlgADP, p25crypto.AlgDESOFB, p25crypto.AlgTDES2, p25crypto.AlgTDES, p25crypto.AlgAES128, p25crypto.AlgAES256} {
		key := make([]byte, VoiceKeyBytes(alg))
		for i := range key {
			key[i] = byte(0x5A ^ i)
		}
		ks, err := VoiceSuperframeKeystream(alg, key, mi)
		if err != nil {
			t.Fatalf("alg 0x%02X: %v", alg, err)
		}
		for _, duid := range []DUID{DUIDLogicalLink1, DUIDLogicalLink2} {
			var clear, work [LDUVoiceSubframeCount][]byte
			for i := range clear {
				if i == 3 {
					continue
				}
				f := make([]byte, 11)
				for j := range f {
					f[j] = byte(i*11 + j)
				}
				clear[i] = f
				work[i] = append([]byte(nil), f...)
			}
			if n, err := DescrambleVoiceFrames(alg, ks, duid, &work); err != nil || n != 8 {
				t.Fatalf("alg 0x%02X scramble: n=%d err=%v", alg, n, err)
			}
			changed := 0
			for i := range work {
				if work[i] != nil && !bytes.Equal(work[i], clear[i]) {
					changed++
				}
			}
			if changed < 7 {
				t.Errorf("alg 0x%02X %v: only %d/8 frames changed by the keystream", alg, duid, changed)
			}
			if _, err := DescrambleVoiceFrames(alg, ks, duid, &work); err != nil {
				t.Fatal(err)
			}
			for i := range work {
				if work[i] == nil {
					if clear[i] != nil {
						t.Errorf("slot %d lost", i)
					}
					continue
				}
				if !bytes.Equal(work[i], clear[i]) {
					t.Errorf("alg 0x%02X %v u%d did not round-trip", alg, duid, i)
				}
			}
		}
	}
	// Unsupported algorithm refused.
	var fs [LDUVoiceSubframeCount][]byte
	if _, err := DescrambleVoiceFrames(0x80, make([]byte, VoiceSuperframeKeystreamBytes), DUIDLogicalLink1, &fs); err == nil {
		t.Error("clear ALGID accepted")
	}
}
