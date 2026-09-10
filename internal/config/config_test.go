package config

import (
	"os"
	"path/filepath"
	"testing"
)

// withTempHome points UserConfigDir-derived paths at a temp dir for the
// duration of the test, so we never touch the real ~/.config.
func withTempHome(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("HOME", dir) // fallback path os.UserConfigDir uses on some OSes
}

func TestSaveLoadRoundTrip(t *testing.T) {
	withTempHome(t)

	want := Config{PAT: "patABC123", BaseID: "appXYZ", Table: "Tasks", Accent: "#FF6AC1"}
	if err := Save(want); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got != want {
		t.Errorf("round trip mismatch:\n got  %+v\n want %+v", got, want)
	}
}

func TestSavePermissions(t *testing.T) {
	withTempHome(t)

	if err := Save(Config{PAT: "p", BaseID: "b", Table: "t"}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	path, err := Path()
	if err != nil {
		t.Fatalf("Path: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("config file permissions = %o, want 0600 (secrets must not be group/world readable)", perm)
	}

	dirInfo, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatalf("Stat dir: %v", err)
	}
	if perm := dirInfo.Mode().Perm(); perm != 0o700 {
		t.Errorf("config dir permissions = %o, want 0700", perm)
	}
}

func TestLoadMissingConfigNoEnv(t *testing.T) {
	withTempHome(t)

	if _, err := Load(); err == nil {
		t.Error("Load with no config file and no env vars: want error, got nil")
	}
}

func TestLoadEnvVarsOverrideFile(t *testing.T) {
	withTempHome(t)

	if err := Save(Config{PAT: "file-pat", BaseID: "file-base", Table: "file-table"}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	t.Setenv("AIRTABLE_PAT", "env-pat")
	t.Setenv("AIRTABLE_TABLE", "env-table")
	// AIRTABLE_BASE intentionally left unset: file's value should survive.

	got, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.PAT != "env-pat" {
		t.Errorf("PAT = %q, want env override %q", got.PAT, "env-pat")
	}
	if got.Table != "env-table" {
		t.Errorf("Table = %q, want env override %q", got.Table, "env-table")
	}
	if got.BaseID != "file-base" {
		t.Errorf("BaseID = %q, want file value %q (no env override set)", got.BaseID, "file-base")
	}
}

func TestLoadEnvVarsAloneAreEnough(t *testing.T) {
	withTempHome(t) // no file written at all

	t.Setenv("AIRTABLE_PAT", "p")
	t.Setenv("AIRTABLE_BASE", "b")
	t.Setenv("AIRTABLE_TABLE", "t")

	got, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.PAT != "p" || got.BaseID != "b" || got.Table != "t" {
		t.Errorf("Load from env only = %+v, want {PAT:p BaseID:b Table:t}", got)
	}
}
