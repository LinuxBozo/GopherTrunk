package p25crypto

import (
	"bytes"
	"testing"
)

func xor(a, b []byte) []byte {
	out := make([]byte, len(a))
	for i := range a {
		out[i] = a[i] ^ b[i]
	}
	return out
}

// TestKeystreamRoundTrip confirms each supported algorithm produces a
// deterministic keystream that decrypts what it encrypts.
func TestKeystreamRoundTrip(t *testing.T) {
	t.Parallel()
	mi := []byte{1, 2, 3, 4, 5, 6, 7, 8, 9}
	pt := []byte("THE QUICK BROWN FOX JUMPS OVER THE LAZY DOG 0123456789")
	cases := []struct {
		alg uint8
		key []byte
	}{
		{AlgADP, []byte{0x11, 0x22, 0x33, 0x44, 0x55}},
		{AlgDESOFB, []byte{1, 2, 3, 4, 5, 6, 7, 8}},
		{AlgTDES2, bytes.Repeat([]byte{0x3C}, 16)},
		{AlgTDES, bytes.Repeat([]byte{0x5A}, 24)},
		{AlgAES128, bytes.Repeat([]byte{0xAB}, 16)},
		{AlgAES256, bytes.Repeat([]byte{0xCD}, 32)},
	}
	for _, c := range cases {
		ks, err := Keystream(c.alg, c.key, mi, len(pt))
		if err != nil {
			t.Fatalf("%s: %v", AlgName(c.alg), err)
		}
		if len(ks) != len(pt) {
			t.Fatalf("%s: keystream len %d, want %d", AlgName(c.alg), len(ks), len(pt))
		}
		ct := xor(pt, ks)
		if bytes.Equal(ct, pt) {
			t.Fatalf("%s: ciphertext equals plaintext (zero keystream?)", AlgName(c.alg))
		}
		// Same key+MI must reproduce the keystream and decrypt.
		ks2, _ := Keystream(c.alg, c.key, mi, len(pt))
		if !bytes.Equal(ks, ks2) {
			t.Fatalf("%s: keystream not deterministic", AlgName(c.alg))
		}
		if got := xor(ct, ks2); !bytes.Equal(got, pt) {
			t.Fatalf("%s: decrypt mismatch:\n got %q\nwant %q", AlgName(c.alg), got, pt)
		}
	}
}

func TestKeystreamRejectsBadKeyLen(t *testing.T) {
	t.Parallel()
	if _, err := Keystream(AlgDESOFB, []byte{1, 2, 3}, []byte{1, 2, 3, 4, 5, 6, 7, 8}, 16); err == nil {
		t.Fatal("expected error for wrong DES key length")
	}
	if _, err := Keystream(0x00, []byte{1}, nil, 8); err == nil {
		t.Fatal("expected error for unsupported algorithm")
	}
}

func TestDifferentMIDifferentKeystream(t *testing.T) {
	t.Parallel()
	key := []byte{0x11, 0x22, 0x33, 0x44, 0x55}
	a, _ := Keystream(AlgADP, key, []byte{1, 2, 3, 4, 5, 6, 7, 8, 9}, 32)
	b, _ := Keystream(AlgADP, key, []byte{9, 8, 7, 6, 5, 4, 3, 2, 1}, 32)
	if bytes.Equal(a, b) {
		t.Fatal("distinct MIs produced the same keystream")
	}
}

func TestDefaultKeysSized(t *testing.T) {
	t.Parallel()
	for _, alg := range []uint8{AlgADP, AlgDESOFB, AlgTDES2, AlgTDES, AlgAES128, AlgAES256} {
		keys := DefaultKeys(alg)
		if len(keys) == 0 {
			t.Fatalf("%s: no default keys", AlgName(alg))
		}
		for _, k := range keys {
			if len(k) != KeySize(alg) {
				t.Fatalf("%s: default key size %d, want %d", AlgName(alg), len(k), KeySize(alg))
			}
		}
	}
}

// TestExpandMIMatchesOP25 transcribes OP25's expand_mi_to_128 (overflow bits
// shifted out over 64 clocks form IV[0:8], the register left behind forms
// IV[8:16]) independently of ExpandMI and checks the two agree on literal
// MIs, including that IV[0:8] is the MI itself.
func TestExpandMIMatchesOP25(t *testing.T) {
	ref := func(mi []byte) []byte {
		var lfsr uint64
		for i := 0; i < 8; i++ {
			lfsr = lfsr<<8 + uint64(mi[i])
		}
		var overflow uint64
		for i := 0; i < 64; i++ {
			ov := (lfsr >> 63) & 1
			fb := ((lfsr >> 63) ^ (lfsr >> 61) ^ (lfsr >> 45) ^ (lfsr >> 37) ^ (lfsr >> 26) ^ (lfsr >> 14)) & 1
			lfsr = lfsr<<1 | fb
			overflow = overflow<<1 | ov
		}
		iv := make([]byte, 16)
		for i := 7; i >= 0; i-- {
			iv[i] = byte(overflow)
			overflow >>= 8
		}
		for i := 15; i >= 8; i-- {
			iv[i] = byte(lfsr)
			lfsr >>= 8
		}
		return iv
	}
	for _, mi := range [][]byte{
		{0x17, 0xCE, 0xEC, 0x55, 0x31, 0x0A, 0x74, 0x75},
		{0, 0, 0, 0, 0, 0, 0, 1},
		{0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF},
		{0xC5, 0x25, 0xBC, 0xC7, 0xF7, 0x2B, 0x67, 0xE9},
	} {
		got := ExpandMI(mi)
		want := ref(mi)
		if string(got) != string(want) {
			t.Errorf("ExpandMI(%x) = %x, want %x", mi, got, want)
		}
		if string(got[:8]) != string(mi) {
			t.Errorf("ExpandMI(%x)[0:8] = %x, want the MI itself", mi, got[:8])
		}
	}
	// A zero MI is the LFSR's fixed point: the IV is all zero.
	if z := ExpandMI(make([]byte, 8)); string(z) != string(make([]byte, 16)) {
		t.Errorf("ExpandMI(0) = %x", z)
	}
}
