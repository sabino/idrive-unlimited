package idrivewebdav

import (
	"testing"

	"idrive-unlimited/internal/evs"
)

func TestBuildRootAliasesUsesFriendlyDeviceNames(t *testing.T) {
	nodes := []evs.Node{
		{
			Name:  "Phone Backup_device-123",
			Path:  "/Phone Backup_device-123",
			IsDir: true,
			Device: &evs.DeviceMeta{
				Name: "Phone Backup",
			},
		},
		{
			Name:  "Social Backup_456",
			Path:  "/Social Backup_456",
			IsDir: true,
			Device: &evs.DeviceMeta{
				Name: "Social Backup",
			},
		},
		{
			Name:  "Test",
			Path:  "/Test",
			IsDir: true,
		},
	}

	aliases := buildRootAliases(nodes)

	if got := aliases.rawToDisplay["Phone Backup_device-123"]; got != "Phone Backup" {
		t.Fatalf("friendly alias mismatch: got %q", got)
	}
	if got := aliases.displayToRaw["Social Backup"]; got != "Social Backup_456" {
		t.Fatalf("reverse alias mismatch: got %q", got)
	}
	if _, ok := aliases.protectedRaw["Social Backup_456"]; !ok {
		t.Fatalf("expected device-backed root to be rename-protected")
	}
}

func TestBuildRootAliasesFallsBackOnConflicts(t *testing.T) {
	nodes := []evs.Node{
		{
			Name:  "Cloud_device-id",
			Path:  "/Cloud_device-id",
			IsDir: true,
			Device: &evs.DeviceMeta{
				Name: "Cloud",
			},
		},
		{
			Name:  "Cloud",
			Path:  "/Cloud",
			IsDir: true,
		},
	}

	aliases := buildRootAliases(nodes)

	if got := aliases.rawToDisplay["Cloud_device-id"]; got != "Cloud_device-id" {
		t.Fatalf("expected raw name fallback, got %q", got)
	}
	if _, exists := aliases.displayToRaw["Cloud"]; exists {
		t.Fatalf("expected conflicting display alias to be rejected")
	}
}

func TestSplitFirstComponent(t *testing.T) {
	head, tail := splitFirstComponent("/Phone Backup/Photos/Vacation")
	if head != "Phone Backup" || tail != "/Photos/Vacation" {
		t.Fatalf("splitFirstComponent() = %q, %q", head, tail)
	}
}

func TestIsProtectedRootRenamePath(t *testing.T) {
	aliases := rootAliasSet{
		rawToDisplay: map[string]string{
			"Phone Backup_device-123": "Phone Backup",
		},
		displayToRaw: map[string]string{
			"Phone Backup": "Phone Backup_device-123",
		},
		protectedRaw: map[string]struct{}{
			"Phone Backup_device-123": {},
		},
	}

	if !isProtectedRootRenamePath(aliases, "/Phone Backup", "/Renamed Phone") {
		t.Fatalf("expected root device rename to be blocked")
	}
	if isProtectedRootRenamePath(aliases, "/Phone Backup/Photos", "/Phone Backup/Renamed Photos") {
		t.Fatalf("expected nested rename to stay allowed")
	}
	if isProtectedRootRenamePath(aliases, "/Cloud", "/Cloud Renamed") {
		t.Fatalf("expected ordinary root folder rename to stay allowed")
	}
	if isProtectedRootRenamePath(aliases, "/Phone Backup", "/Phone Backup") {
		t.Fatalf("expected no-op rename to stay allowed")
	}
}
