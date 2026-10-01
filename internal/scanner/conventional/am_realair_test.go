package conventional

import "testing"

// TestAMSquelchOnRealAirCarrier is the #1219 on-air pin: the reporter's
// 126.400 MHz (KRIC Potomac Departure) capture, carrier up for its first
// 1.5 s and receiver noise only after the transmitter unkeys, both slices
// from the same receiver at the same gain. The carrier-to-noise squelch
// must hold open through the whole transmission (voice sidebands and the
// carrier's own fading included) and must never reach even the CLOSE level
// on the noise that follows.
func TestAMSquelchOnRealAirCarrier(t *testing.T) {
	lo, hi := meterRange(loadRealAirAt2400k(t, "am_kric_126400k_carrier_48k.cs16"))
	t.Logf("carrier slice: C/N %.1f..%.1f dB", lo, hi)
	if lo < DefaultAMSquelchCNDb {
		t.Errorf("C/N dipped to %.1f dB during the transmission, want >= %.1f (squelch open)", lo, DefaultAMSquelchCNDb)
	}
	lo, hi = meterRange(loadRealAirAt2400k(t, "am_kric_126400k_noise_48k.cs16"))
	t.Logf("noise slice: C/N %.1f..%.1f dB", lo, hi)
	if closeAt := DefaultAMSquelchCNDb - defaultSquelchHysteresisDb; hi >= closeAt {
		t.Errorf("C/N reached %.1f dB on receiver noise, want < %.1f (squelch closed)", hi, closeAt)
	}
}
