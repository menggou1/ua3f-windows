package main

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestSaveFailurePreservesConfigOnDiskAndInMemory(t *testing.T) {
	app, err := NewApp(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	old := DefaultAppConfig()
	if err := app.SaveConfig(old); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(app.configPath, 0444); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(app.configPath, 0644)
	changed := old
	changed.TTL = 65
	if err := app.SaveConfig(changed); err == nil {
		t.Fatal("read-only replacement should fail")
	}
	disk, err := LoadAppConfig(app.configPath)
	if err != nil {
		t.Fatal(err)
	}
	if disk != old || app.LoadConfig() != old {
		t.Fatal("failed save changed the previous configuration")
	}
	leftovers, _ := filepath.Glob(filepath.Join(app.exeDir, ".ua3f-config-*"))
	if len(leftovers) != 0 {
		t.Fatalf("temporary files leaked: %v", leftovers)
	}
}

func TestConcurrentConfigSavesPublishTheLastCompleteFile(t *testing.T) {
	app, err := NewApp(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	failures := make(chan error, 24)
	for i := 0; i < 24; i++ {
		cfg := DefaultAppConfig()
		cfg.TTL = uint8(i + 1)
		if i%2 == 0 {
			cfg.Preset = "pc"
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := app.SaveConfig(cfg); err != nil {
				failures <- err
			}
		}()
	}
	wg.Wait()
	close(failures)
	for err := range failures {
		t.Error(err)
	}
	disk, err := LoadAppConfig(app.configPath)
	if err != nil {
		t.Fatal(err)
	}
	if disk != app.LoadConfig() {
		t.Fatalf("disk and memory disagree: disk=%+v memory=%+v", disk, app.LoadConfig())
	}
}

func TestConfigSaveRetriesBriefWindowsReadLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := SaveAppConfig(path, DefaultAppConfig()); err != nil {
		t.Fatal(err)
	}
	reader, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	changed := DefaultAppConfig()
	changed.TTL = 65
	done := make(chan error, 1)
	go func() { done <- SaveAppConfig(path, changed) }()
	// A normal Go Windows read handle denies delete/rename sharing.
	select {
	case err := <-done:
		t.Fatalf("save ended before the read lock was released: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("save did not complete after lock release")
	}
	disk, err := LoadAppConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if disk.TTL != 65 {
		t.Fatal("replacement was not published")
	}
}

func TestRunningAndSavedConfigurationsRemainDistinct(t *testing.T) {
	app, err := NewApp(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	old := app.LoadConfig() // a start request may have captured this before a save
	changed := old
	changed.TTL = 65
	if err := app.SaveConfig(changed); err != nil {
		t.Fatal(err)
	}
	app.runner.open = func() (packetHandle, error) { return newFakeHandle(), nil }
	defer func() { _ = app.Stop(); waitRunner(t, app.runner) }()
	if err := app.runner.Start(old); err != nil {
		t.Fatal(err)
	}
	snapshot := app.Snapshot()
	if snapshot.Config.TTL != 65 || snapshot.RunningConfig == nil || snapshot.RunningConfig.TTL != 64 || !snapshot.ConfigPending {
		t.Fatalf("starting a captured configuration lost the newer save: %+v", snapshot)
	}
	disk, err := LoadAppConfig(app.configPath)
	if err != nil {
		t.Fatal(err)
	}
	if disk != snapshot.Config {
		t.Fatal("start reset the in-memory saved configuration")
	}
	snapshot.RunningConfig.TTL = 99
	if app.Snapshot().RunningConfig.TTL != 64 {
		t.Fatal("snapshot pointer exposed mutable application state")
	}
	if err := app.Stop(); err != nil {
		t.Fatal(err)
	}
	waitRunner(t, app.runner)
	stopped := app.Snapshot()
	if stopped.RunningConfig != nil || stopped.ConfigPending {
		t.Fatal("stopped session still appears active")
	}
	if err := app.Start(); err != nil {
		t.Fatal(err)
	}
	restarted := app.Snapshot()
	if restarted.RunningConfig == nil || restarted.RunningConfig.TTL != 65 || restarted.ConfigPending {
		t.Fatalf("restart did not use saved config: %+v", restarted)
	}
}

func TestSavingDuringDriverInitializationDoesNotChangeSessionConfig(t *testing.T) {
	app, err := NewApp(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	app.runner.open = func() (packetHandle, error) { close(entered); <-release; return newFakeHandle(), nil }
	defer func() { _ = app.Stop(); waitRunner(t, app.runner) }()
	go func() { done <- app.Start() }()
	<-entered
	changed := DefaultAppConfig()
	changed.TTL = 65
	saveErr := app.SaveConfig(changed)
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if saveErr != nil {
		t.Fatal(saveErr)
	}
	snapshot := app.Snapshot()
	if snapshot.Config.TTL != 65 || snapshot.RunningConfig.TTL != 64 || !snapshot.ConfigPending {
		t.Fatalf("unexpected startup config: %+v", snapshot)
	}
}
