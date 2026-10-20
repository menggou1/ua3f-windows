package main

import (
	"context"
	"fmt"
	"log"
	"path/filepath"
	"sync"

	"github.com/williamfhe/godivert"
)

type packetHandle interface {
	Recv() (*godivert.Packet, error)
	Send(*godivert.Packet) (uint, error)
	HelperCalcChecksum(*godivert.Packet)
	Close() error
}
type runnerSession struct {
	done      chan struct{}
	cancelled bool         // guarded by Runner.mu
	handle    packetHandle // published under Runner.mu, then immutable
	closeMu   sync.Mutex
	closed    bool
	closeErr  error
	finished  bool // guarded by Runner.mu
}

func (s *runnerSession) close() error {
	s.closeMu.Lock()
	defer s.closeMu.Unlock()
	if s.closed {
		return nil
	}
	s.closeErr = s.handle.Close()
	s.closed = s.closeErr == nil
	return s.closeErr
}

func (s *runnerSession) closeError() error {
	s.closeMu.Lock()
	defer s.closeMu.Unlock()
	return s.closeErr
}

type Runner struct {
	mu     sync.Mutex
	exeDir string
	state  *AppState
	run    *runnerSession
	open   func() (packetHandle, error)
}

func NewRunner(exeDir string, state *AppState) *Runner {
	r := &Runner{exeDir: exeDir, state: state}
	r.open = r.openDriver
	return r
}
func (r *Runner) openDriver() (packetHandle, error) {
	if err := prepareWinDivertFiles(r.exeDir); err != nil {
		return nil, err
	}
	diagnostics, err := diagnoseDriverServices(r.exeDir)
	if err != nil {
		r.logf("[warn] driver service diagnosis: %v", err)
	}
	for _, d := range diagnostics {
		r.logf("[*] service=%s running=%t path=%q expected=%q: %s", d.Service, d.Running, d.RegisteredPath, d.ExpectedPath, d.Message)
	}
	dllPath := filepath.Join(r.exeDir, "WinDivert.dll")
	if err := godivert.LoadDLLChecked(dllPath, dllPath); err != nil {
		return nil, err
	}
	h, err := godivert.NewWinDivertHandle("outbound && ip")
	if err != nil {
		return nil, fmt.Errorf("WinDivertOpen failed (see driver service diagnostics and README): %w", err)
	}
	return h, nil
}
func (r *Runner) Start(cfg AppConfig) error {
	r.mu.Lock()
	if r.run != nil {
		err := r.run.closeError()
		r.mu.Unlock()
		return err
	}
	s := &runnerSession{done: make(chan struct{})}
	r.run = s
	r.state.SetConfig(cfg.Normalize())
	r.state.SetCounters(0, 0, 0)
	r.state.SetStatus(StatusStarting, "")
	r.mu.Unlock()
	r.logf("[+] starting WinDivert")
	h, err := r.open()
	if err != nil {
		r.finish(s, err)
		return err
	}
	r.mu.Lock()
	s.handle = h
	if s.cancelled {
		r.mu.Unlock()
		err := s.close()
		r.finish(s, err)
		return err
	}
	r.state.SetStatus(StatusRunning, "")
	r.mu.Unlock()
	r.logf("[+] WinDivert running")
	go r.loop(s, cfg.ToRuntimeConfig())
	return nil
}
func (r *Runner) loop(s *runnerSession, cfg *Config) {
	var pktCount, ttlCount, uaCount uint64
	var loopErr error
	defer func() {
		if recovered := recover(); recovered != nil {
			loopErr = fmt.Errorf("packet loop panic: %v", recovered)
		}
		closeErr := s.close()
		if closeErr != nil {
			loopErr = fmt.Errorf("driver close failed: %w", closeErr)
		}
		r.finish(s, loopErr)
	}()
	for {
		pkt, err := s.handle.Recv()
		if err != nil {
			loopErr = err
			return
		}
		pktCount++
		ttlChanged, uaChanged := rewrite(pkt, cfg)
		if ttlChanged {
			ttlCount++
		}
		if uaChanged {
			uaCount++
		}
		if ttlChanged || uaChanged {
			s.handle.HelperCalcChecksum(pkt)
		}
		if _, err := s.handle.Send(pkt); err != nil {
			loopErr = fmt.Errorf("send failed: %w", err)
			return
		}
		r.state.SetCounters(pktCount, ttlCount, uaCount)
		if cfg.Log == "debug" && pktCount%5000 == 0 {
			r.logf("[debug] packets=%d ttl_rewrites=%d ua_rewrites=%d", pktCount, ttlCount, uaCount)
		}
	}
}
func (r *Runner) Stop() error {
	r.mu.Lock()
	s := r.run
	if s == nil {
		r.mu.Unlock()
		return nil
	}
	s.cancelled = true
	r.state.SetStatus(StatusStopping, "")
	h := s.handle
	r.mu.Unlock()
	if h != nil {
		err := s.close()
		r.mu.Lock()
		finished := s.finished
		r.mu.Unlock()
		if finished {
			r.finish(s, err)
		} else if err != nil {
			r.mu.Lock()
			if r.run == s {
				r.state.SetStatus(StatusError, err.Error())
			}
			r.mu.Unlock()
		}
		return err
	}
	return nil // Start owns cleanup when driver initialization completes.
}
func (r *Runner) Wait(ctx context.Context) error {
	r.mu.Lock()
	s := r.run
	r.mu.Unlock()
	if s == nil {
		return nil
	}
	select {
	case <-s.done:
		return s.closeError()
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (r *Runner) finish(s *runnerSession, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.run != s {
		return
	}
	closeErr := s.closeError()
	if closeErr != nil {
		err = closeErr
	}
	if err != nil && (!s.cancelled || closeErr != nil) {
		r.state.SetStatus(StatusError, err.Error())
		r.logf("[-] WinDivert ended: %v", err)
	} else {
		r.state.SetStatus(StatusIdle, "")
		r.logf("[*] WinDivert stopped")
	}
	// A failed Close reserves this session until Stop successfully retries it.
	// Never create a new handle while the old one may still divert traffic.
	if closeErr == nil {
		r.run = nil
	}
	if !s.finished {
		close(s.done)
	}
	s.finished = true
}
func (r *Runner) logf(format string, args ...any) {
	line := fmt.Sprintf(format, args...)
	log.Print(line)
	r.state.AppendLog(line)
}
