package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDriverPathDiagnosis(t *testing.T) {
	dir := t.TempDir()
	current := filepath.Join(dir, "WinDivert64.sys")
	if err := os.WriteFile(current, []byte("driver"), 0644); err != nil {
		t.Fatal(err)
	}
	d := assessDriverService("WinDivert", `\??\`+current, dir, true)
	if d.PathMismatch || d.Missing || !d.Running {
		t.Fatalf("incorrect current path diagnosis: %+v", d)
	}
	d = assessDriverService("WinDivert", filepath.Join(dir, "old", "WinDivert64.sys"), dir, false)
	if !d.PathMismatch || !d.Missing {
		t.Fatalf("moved directory not detected: %+v", d)
	}
	other := filepath.Join(t.TempDir(), "WinDivert64.sys")
	if err := os.WriteFile(other, []byte("driver"), 0644); err != nil {
		t.Fatal(err)
	}
	d = assessDriverService("WinDivert", `"`+other+`"`, dir, true)
	if !d.PathMismatch || d.Missing || !d.Running {
		t.Fatalf("other application's driver misclassified: %+v", d)
	}
	t.Setenv("SystemRoot", dir)
	for _, path := range []string{`%SystemRoot%\WinDivert64.sys`, `\SystemRoot\WinDivert64.sys`} {
		if got := normalizeDriverPath(path); got != current {
			t.Fatalf("normalize %q got %q", path, got)
		}
	}
}
