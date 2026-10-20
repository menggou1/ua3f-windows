package main

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func readSessionToken(t *testing.T, url string) string {
	t.Helper()
	resp, err := http.Get(url + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	page, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	marker := []byte(`<meta name="ua3f-token" content="`)
	_, tail, ok := bytes.Cut(page, marker)
	if !ok {
		t.Fatal("control page is missing session token")
	}
	token, _, ok := bytes.Cut(tail, []byte(`"`))
	if !ok || len(token) != 64 {
		t.Fatal("invalid token in control page")
	}
	if resp.Header.Get("Cache-Control") != "no-store" {
		t.Fatal("control page must not cache tokens")
	}
	return string(token)
}
func authenticatedRequest(t *testing.T, url, method, path string, body io.Reader) (*http.Response, error) {
	t.Helper()
	req, err := http.NewRequest(method, url+path, body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-UA3F-Token", readSessionToken(t, url))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", url)
	return http.DefaultClient.Do(req)
}
func TestLocalAPISecurity(t *testing.T) {
	app, err := NewApp(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	server, url, quit, err := startWebServer(app)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	token := readSessionToken(t, url)
	cases := []struct {
		name, path, method, token, origin, host, site, contentType, body string
		want                                                             int
	}{
		{name: "missing token", path: "/api/quit", method: "POST", want: 403},
		{name: "invalid token", path: "/api/quit", method: "POST", token: "wrong", want: 403},
		{name: "foreign origin", path: "/api/quit", method: "POST", token: token, origin: "https://example.com", want: 403},
		{name: "null origin", path: "/api/quit", method: "POST", token: token, origin: "null", want: 403},
		{name: "DNS rebinding", path: "/api/quit", method: "POST", token: token, host: "evil.example", want: 403},
		{name: "cross site", path: "/api/quit", method: "POST", token: token, site: "cross-site", want: 403},
		{name: "protected reads", path: "/api/status", method: "GET", want: 403},
		{name: "read method", path: "/api/status", method: "POST", token: token, want: 405},
		{name: "quit method", path: "/api/quit", method: "GET", token: token, want: 405},
		{name: "form post", path: "/api/config", method: "POST", token: token, contentType: "text/plain", body: `{"ttl":65}`, want: 415},
		{name: "oversized", path: "/api/config", method: "POST", token: token, contentType: "application/json", body: `{"ua":"` + strings.Repeat("a", 17000) + `"}`, want: 413},
		{name: "trailing json", path: "/api/config", method: "POST", token: token, contentType: "application/json", body: `{"ttl":65} {}`, want: 400},
		{name: "malformed json", path: "/api/config", method: "POST", token: token, contentType: "application/json", body: `{"ttl":`, want: 400},
		{name: "valid read", path: "/api/status", method: "GET", token: token, origin: url, want: 200},
		{name: "valid config", path: "/api/config", method: "POST", token: token, origin: url, contentType: "application/json", body: `{"ttl":65}`, want: 200},
		{name: "diagnostics", path: "/api/diagnostics", method: "GET", token: token, want: 200},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req, err := http.NewRequest(tc.method, url+tc.path, strings.NewReader(tc.body))
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("X-UA3F-Token", tc.token)
			req.Header.Set("Origin", tc.origin)
			req.Header.Set("Sec-Fetch-Site", tc.site)
			req.Header.Set("Content-Type", tc.contentType)
			if tc.host != "" {
				req.Host = tc.host
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != tc.want {
				b, _ := io.ReadAll(resp.Body)
				t.Fatalf("got %d want %d: %s", resp.StatusCode, tc.want, b)
			}
			select {
			case <-quit:
				t.Fatal("unauthorized request quit app")
			default:
			}
		})
	}
}
func TestSessionTokenRotatesAcrossServers(t *testing.T) {
	app, err := NewApp(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	first, a, _, err := startWebServer(app)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, b, _, err := startWebServer(app)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if readSessionToken(t, a) == readSessionToken(t, b) {
		t.Fatal("session token reused")
	}
}
func TestHostProtectionIncludesPage(t *testing.T) {
	called := false
	handler := protectLocalAPI(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }), "127.0.0.1:18080", "token")
	req := httptest.NewRequest("GET", "http://evil.example/", nil)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	if called || recorder.Code != 403 {
		t.Fatal("foreign host reached token-bearing page")
	}
}

func TestAuthenticatedControlLifecycle(t *testing.T) {
	app, err := NewApp(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	app.runner.open = func() (packetHandle, error) { return newFakeHandle(), nil }
	server, url, quit, err := startWebServer(app)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	defer func() { _ = app.Stop(); waitRunner(t, app.runner) }()
	for _, step := range []struct{ path, body string }{
		{"/api/start", ""},
		{"/api/config", `{"preset":"pc","ttl":65,"log":"debug"}`},
		{"/api/stop", ""},
		{"/api/start", ""},
		{"/api/stop", ""},
		{"/api/quit", ""},
	} {
		resp, err := authenticatedRequest(t, url, "POST", step.path, strings.NewReader(step.body))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatalf("%s returned %d", step.path, resp.StatusCode)
		}
		switch step.path {
		case "/api/start":
			if app.Snapshot().Status != StatusRunning {
				t.Fatal("start did not enter running state")
			}
		case "/api/stop":
			waitRunner(t, app.runner)
			if app.Snapshot().Status != StatusIdle {
				t.Fatal("stop did not reach idle")
			}
		}
	}
	if app.LoadConfig().TTL != 65 || app.LoadConfig().Preset != "pc" {
		t.Fatal("restart lost saved config")
	}
	select {
	case <-quit:
	default:
		t.Fatal("quit not signaled")
	}
}
