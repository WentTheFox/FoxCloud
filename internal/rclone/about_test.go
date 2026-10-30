package rclone

import "testing"

func TestParseAbout(t *testing.T) {
	u, err := ParseAbout([]byte(`{"total":1000,"used":250,"free":750}`))
	if err != nil || u != (Usage{Total: 1000, Used: 250, Free: 750}) {
		t.Fatalf("ParseAbout = %+v, %v", u, err)
	}
	if got := u.UsedPercent(); got != 25 {
		t.Errorf("UsedPercent = %v, want 25", got)
	}
}

func TestParseAboutDerivesFree(t *testing.T) {
	u, err := ParseAbout([]byte(`{"total":1000,"used":400}`))
	if err != nil || u.Free != 600 {
		t.Errorf("ParseAbout = %+v, %v", u, err)
	}
}

func TestParseAboutMissingFields(t *testing.T) {
	if _, err := ParseAbout([]byte(`{"free":5}`)); err == nil {
		t.Error("expected an error when total/used are missing")
	}
	if _, err := ParseAbout([]byte(`not json`)); err == nil {
		t.Error("expected an error for bad JSON")
	}
}

func TestUsedPercentUnknownTotal(t *testing.T) {
	if got := (Usage{}).UsedPercent(); got != 0 {
		t.Errorf("UsedPercent = %v, want 0", got)
	}
}
