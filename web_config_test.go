package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
)

func TestConfigAPIRejectsInvalidValuesWithoutChangingSavedFile(t *testing.T) {
	app, err := NewApp(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	initial := DefaultAppConfig()
	if err := app.SaveConfig(initial); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(app.configPath)
	if err != nil {
		t.Fatal(err)
	}
	server, url, _, err := startWebServer(app)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	for _, body := range []string{
		`null`, `{}`, `{"ttl":0}`, `{"ttl":-1}`, `{"ttl":256}`, `{"ttl":64.5}`, `{"ttl":"65"}`,
		`{"ttl":65,"preset":"unknown"}`, `{"ttl":65,"log":"verbose"}`,
	} {
		t.Run(body, func(t *testing.T) {
			resp, err := authenticatedRequest(t, url, http.MethodPost, "/api/config", strings.NewReader(body))
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != 400 {
				t.Fatalf("invalid config accepted with status %d", resp.StatusCode)
			}
			var result map[string]string
			if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
				t.Fatal(err)
			}
			if result["error"] == "" {
				t.Fatal("validation error missing")
			}
			after, err := os.ReadFile(app.configPath)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, after) || app.LoadConfig() != initial {
				t.Fatal("rejected input changed saved config")
			}
		})
	}
	resp, err := authenticatedRequest(t, url, http.MethodPost, "/api/config", strings.NewReader(`{"ttl":65,"preset":" PC ","log":" Debug "}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("supported values rejected: %s", body)
	}
	saved := app.LoadConfig()
	if saved.TTL != 65 || saved.Preset != "pc" || saved.Log != "debug" {
		t.Fatalf("supported values not normalized: %+v", saved)
	}
}

func TestStatusAPIReportsPendingConfigUntilRestart(t *testing.T) {
	app, err := NewApp(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	app.runner.open = func() (packetHandle, error) { return newFakeHandle(), nil }
	server, url, _, err := startWebServer(app)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	defer func() { _ = app.Stop(); waitRunner(t, app.runner) }()
	post := func(path, body string) {
		t.Helper()
		resp, err := authenticatedRequest(t, url, "POST", path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatalf("%s returned %d", path, resp.StatusCode)
		}
	}
	snapshot := func() AppSnapshot {
		t.Helper()
		resp, err := authenticatedRequest(t, url, "GET", "/api/status", nil)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var s AppSnapshot
		if err := json.NewDecoder(resp.Body).Decode(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	post("/api/start", "")
	post("/api/config", `{"ttl":65}`)
	pending := snapshot()
	if pending.Config.TTL != 65 || pending.RunningConfig == nil || pending.RunningConfig.TTL != 64 || !pending.ConfigPending {
		t.Fatalf("missing pending state: %+v", pending)
	}
	post("/api/stop", "")
	waitRunner(t, app.runner)
	if s := snapshot(); s.RunningConfig != nil || s.ConfigPending {
		t.Fatalf("stopped config still active: %+v", s)
	}
	post("/api/start", "")
	if s := snapshot(); s.RunningConfig == nil || s.RunningConfig.TTL != 65 || s.ConfigPending {
		t.Fatalf("new config not active: %+v", s)
	}
}
