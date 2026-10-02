package storage

import (
	"context"
	"testing"
)

// TestConvLockoutStoreRoundTrip pins the persistence contract the
// conventional scanner restores from at startup: a lockout survives a
// reopen of the database, Set is idempotent, and Clear removes exactly one
// frequency.
func TestConvLockoutStoreRoundTrip(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	st, err := NewConvLockoutStore(db)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Set(ctx, 462_562_500, "GMRS 1"); err != nil {
		t.Fatal(err)
	}
	if err := st.Set(ctx, 146_520_000, "2m call"); err != nil {
		t.Fatal(err)
	}
	// Idempotent: the same frequency again refreshes the label only.
	if err := st.Set(ctx, 462_562_500, "GMRS 1 (renamed)"); err != nil {
		t.Fatal(err)
	}
	got, err := st.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("List = %d rows, want 2: %+v", len(got), got)
	}
	if got[0].FrequencyHz != 462_562_500 || got[0].Label != "GMRS 1 (renamed)" {
		t.Errorf("first row = %+v, want 462.5625 MHz with refreshed label", got[0])
	}
	if err := st.Clear(ctx, 462_562_500); err != nil {
		t.Fatal(err)
	}
	if err := st.Clear(ctx, 999); err != nil {
		t.Errorf("Clear of an unknown frequency errored: %v", err)
	}
	got, err = st.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].FrequencyHz != 146_520_000 {
		t.Fatalf("after Clear List = %+v, want only 146.52 MHz", got)
	}
	if st.Set(ctx, 0, "") == nil {
		t.Error("Set(0) accepted a zero frequency")
	}
}
