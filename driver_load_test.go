package main

import (
	"path/filepath"
	"syscall"
	"testing"

	"github.com/williamfhe/godivert"
)

func TestLoadDriverLibraryReturnsErrorWithoutPanic(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.dll")
	if err := godivert.LoadDLLChecked(path, path); err == nil {
		t.Fatal("missing driver library accepted")
	}
}

// Resolves DLL exports only. This never calls WinDivertOpen or installs a driver.
func TestEmbeddedDriverLibraryExports(t *testing.T) {
	dir := t.TempDir()
	if err := prepareWinDivertFiles(dir); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "WinDivert.dll")
	dll, err := syscall.LoadDLL(path)
	if err != nil {
		t.Fatal(err)
	}
	defer dll.Release()
	for _, name := range []string{"WinDivertOpen", "WinDivertClose", "WinDivertRecv", "WinDivertSend", "WinDivertHelperCalcChecksums"} {
		if _, err := dll.FindProc(name); err != nil {
			t.Fatal(err)
		}
	}
}
