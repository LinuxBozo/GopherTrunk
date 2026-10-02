package trunking

import (
	"sync"
	"testing"
	"time"
)

// TestVoicePoolActiveSnapshotIsRaceFree: a voice chain touches its call on
// every frame while the engine watchdog (and the API) read Active(). With
// Active() handing out the pool's live entries, `make test` (-race) reported
// the LastHeardAt write in Touch against the watchdog's unlocked read — found
// by the #1187 replay end-to-end test. Run under -race to be meaningful.
func TestVoicePoolActiveSnapshotIsRaceFree(t *testing.T) {
	p, _ := mkPool(1)
	if _, err := p.Bind(p.FindFree(), Grant{System: "s", GroupID: 1, FrequencyHz: 851_000_000}, nil, time.Unix(0, 0)); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 2000; i++ {
			p.Touch(serial(0), time.Unix(int64(i), 0))
			p.UpdateSignal(serial(0), float64(-i))
		}
	}()
	go func() {
		defer wg.Done()
		var sink time.Time
		for i := 0; i < 2000; i++ {
			for _, ac := range p.Active() {
				if ac.LastHeardAt.After(sink) {
					sink = ac.LastHeardAt
				}
				_ = ac.SignalDbFS
			}
		}
	}()
	wg.Wait()
	// A snapshot is a copy: mutating it must not reach the pool.
	snap := p.Active()[0]
	snap.LastHeardAt = time.Unix(99_999, 0)
	if p.Active()[0].LastHeardAt.Equal(snap.LastHeardAt) {
		t.Fatal("Active() returned the live entry, not a snapshot")
	}
}
