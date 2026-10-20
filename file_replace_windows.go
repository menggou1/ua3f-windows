//go:build windows

package main

import (
	"errors"
	"os"
	"time"

	"golang.org/x/sys/windows"
)

// Windows readers and antivirus tools may briefly prevent file replacement.
// Retry only sharing/access conflicts, with a bound; never remove the old file.
func replaceConfigFile(src, dst string) error {
	var err error
	for attempt := 0; attempt < 5; attempt++ {
		err = os.Rename(src, dst)
		if err == nil {
			return nil
		}
		if !errors.Is(err, windows.ERROR_SHARING_VIOLATION) &&
			!errors.Is(err, windows.ERROR_LOCK_VIOLATION) &&
			!errors.Is(err, windows.ERROR_ACCESS_DENIED) {
			return err
		}
		if attempt < 4 {
			time.Sleep(10 * time.Millisecond << attempt)
		}
	}
	return err
}
