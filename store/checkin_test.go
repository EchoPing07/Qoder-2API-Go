package store

import (
	"testing"
	"time"
)

func TestCheckinTimeDefaultsAndValidation(t *testing.T) {
	s, err := New(t.TempDir() + "/data.json")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got := s.GetCheckinTime(); got != DefaultCheckinTime {
		t.Errorf("default checkin time = %q, want %q", got, DefaultCheckinTime)
	}
	if err := s.SetCheckinTime("08:15"); err != nil {
		t.Fatalf("SetCheckinTime: %v", err)
	}
	if got := s.GetCheckinTime(); got != "08:15" {
		t.Errorf("checkin time = %q, want 08:15", got)
	}
	if err := s.SetCheckinTime("25:00"); err == nil {
		t.Error("expected an error for an invalid time")
	}
}

func TestUpdateAccountCheckinAndRecord(t *testing.T) {
	s, err := New(t.TempDir() + "/data.json")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	entry, err := s.AddAccountWithRegion("pat-abc", "note", "")
	if err != nil {
		t.Fatalf("AddAccount: %v", err)
	}
	if err := s.UpdateAccountCheckin(entry.ID, true, "07:45"); err != nil {
		t.Fatalf("UpdateAccountCheckin: %v", err)
	}
	acc, _ := s.GetAccount(entry.ID)
	if !acc.AutoCheckin || acc.CheckinTime != "07:45" {
		t.Fatalf("checkin settings not persisted: %+v", acc)
	}
	// Invalid time is rejected.
	if err := s.UpdateAccountCheckin(entry.ID, true, "bad"); err == nil {
		t.Error("expected error for invalid time")
	}
	// Unknown account is rejected.
	if err := s.UpdateAccountCheckin("nope", true, ""); err == nil {
		t.Error("expected error for unknown account")
	}

	at := time.Unix(1700000000, 0)
	if err := s.RecordCheckin(entry.ID, "success", "签到成功 +10 积分", at); err != nil {
		t.Fatalf("RecordCheckin: %v", err)
	}
	acc, _ = s.GetAccount(entry.ID)
	if acc.LastCheckinAt != at.Unix() || acc.LastCheckinStatus != "success" || acc.LastCheckinMsg == "" {
		t.Errorf("record not persisted: %+v", acc)
	}
}

// The default check-in time must survive a reload (persisted to disk).
func TestCheckinTimePersistsAcrossReload(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/data.json"
	s, err := New(path)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := s.SetCheckinTime("06:20"); err != nil {
		t.Fatalf("SetCheckinTime: %v", err)
	}
	s2, err := New(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if got := s2.GetCheckinTime(); got != "06:20" {
		t.Errorf("reloaded checkin time = %q, want 06:20", got)
	}
}
