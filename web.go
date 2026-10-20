package main

import (
	"bytes"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

func startWebServer(app *App) (*http.Server, string, <-chan struct{}, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:18080")
	if err != nil {
		ln, err = net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return nil, "", nil, err
		}
	}
	var tokenBytes [32]byte
	if _, err := rand.Read(tokenBytes[:]); err != nil {
		ln.Close()
		return nil, "", nil, err
	}
	token := hex.EncodeToString(tokenBytes[:])
	address := ln.Addr().String()

	quit := make(chan struct{})
	var quitOnce sync.Once
	mux := http.NewServeMux()
	mux.Handle("/assets/", http.StripPrefix("/assets/", http.FileServer(http.FS(webStaticFS()))))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		page, err := fs.ReadFile(webStaticFS(), "index.html")
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		page = bytes.Replace(page, []byte("</head>"), []byte(`<meta name="ua3f-token" content="`+token+`"></head>`), 1)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if r.Method != http.MethodHead {
			_, _ = w.Write(page)
		}
	})
	mux.HandleFunc("/api/config", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			writeJSON(w, http.StatusOK, app.LoadConfig())
		case http.MethodPost:
			mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
			if err != nil || mediaType != "application/json" {
				writeError(w, http.StatusUnsupportedMediaType, fmt.Errorf("Content-Type must be application/json"))
				return
			}
			var cfg AppConfig
			r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
			if err := decodeJSON(r.Body, &cfg); err != nil {
				status := http.StatusBadRequest
				var limitErr *http.MaxBytesError
				if errors.As(err, &limitErr) {
					status = http.StatusRequestEntityTooLarge
				}
				writeError(w, status, err)
				return
			}
			if err := app.SaveConfig(cfg); err != nil {
				writeError(w, http.StatusInternalServerError, err)
				return
			}
			writeJSON(w, http.StatusOK, app.LoadConfig())
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	})
	mux.HandleFunc("/api/status", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		writeJSON(w, http.StatusOK, app.Snapshot())
	})
	mux.HandleFunc("/api/start", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if err := app.Start(); err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, app.Snapshot())
	})
	mux.HandleFunc("/api/stop", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if err := app.Stop(); err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, app.Snapshot())
	})
	mux.HandleFunc("/api/logs", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		writeJSON(w, http.StatusOK, app.Snapshot().Logs)
	})
	mux.HandleFunc("/api/diagnostics", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		diagnostics, err := diagnoseDriverServices(app.exeDir)
		message := ""
		if err != nil {
			message = err.Error()
		}
		writeJSON(w, http.StatusOK, map[string]any{"services": diagnostics, "error": message})
	})
	mux.HandleFunc("/api/quit", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		quitOnce.Do(func() { close(quit) })
	})

	server := &http.Server{
		Handler:           protectLocalAPI(mux, address, token),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		IdleTimeout:       30 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}
	go func() {
		_ = server.Serve(ln)
	}()

	url := fmt.Sprintf("http://%s", ln.Addr().String())
	return server, url, quit, nil
}

func protectLocalAPI(next http.Handler, address, token string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'")
		if r.Host != address || (r.Header.Get("Origin") != "" && r.Header.Get("Origin") != "http://"+address) {
			writeError(w, http.StatusForbidden, fmt.Errorf("request host or origin is not allowed"))
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") {
			if r.Header.Get("Sec-Fetch-Site") == "cross-site" || subtle.ConstantTimeCompare([]byte(r.Header.Get("X-UA3F-Token")), []byte(token)) != 1 {
				writeError(w, http.StatusForbidden, fmt.Errorf("invalid local session token; reload the control page"))
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

func decodeJSON(body io.ReadCloser, v any) error {
	defer body.Close()
	decoder := json.NewDecoder(body)
	if err := decoder.Decode(v); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err != nil {
			return err
		}
		return fmt.Errorf("expected a single JSON value")
	}
	return nil
}
