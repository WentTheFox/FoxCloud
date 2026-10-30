package state

import (
	"testing"
	"time"
)

func TestStoreRoundTrip(t *testing.T) {
	s := Store{Dir: t.TempDir()}

	got, err := s.Load("data")
	if err != nil || !got.LastSuccess.IsZero() {
		t.Fatalf("empty load = %+v, %v", got, err)
	}

	want := Job{
		LastSuccess: time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC),
		Paused:      &Failure{Kind: "too-many-deletes", Message: "m"},
		Resynced:    true,
	}
	if err := s.Save("data", want); err != nil {
		t.Fatal(err)
	}
	got, err = s.Load("data")
	if err != nil {
		t.Fatal(err)
	}
	if !got.LastSuccess.Equal(want.LastSuccess) || got.Paused == nil || got.Paused.Kind != "too-many-deletes" || !got.Resynced {
		t.Errorf("round trip = %+v", got)
	}
}
