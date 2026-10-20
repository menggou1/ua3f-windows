package main

import (
	"context"
	"errors"
	"github.com/williamfhe/godivert"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakePacketHandle struct {
	closed     chan struct{}
	recving    chan struct{}
	closeCount atomic.Int32
	recvCount  atomic.Int32
	recvErr    error
	panicRecv  bool
}

func newFakeHandle() *fakePacketHandle {
	return &fakePacketHandle{closed: make(chan struct{}), recving: make(chan struct{})}
}
func (h *fakePacketHandle) Recv() (*godivert.Packet, error) {
	h.recvCount.Add(1)
	select {
	case <-h.recving:
	default:
		close(h.recving)
	}
	if h.panicRecv {
		panic("synthetic parser panic")
	}
	if h.recvErr != nil {
		return nil, h.recvErr
	}
	<-h.closed
	return nil, errors.New("handle closed")
}
func (h *fakePacketHandle) Send(*godivert.Packet) (uint, error) { return 0, nil }
func (h *fakePacketHandle) HelperCalcChecksum(*godivert.Packet) {}
func (h *fakePacketHandle) Close() error {
	if h.closeCount.Add(1) == 1 {
		close(h.closed)
	}
	return nil
}
func waitRunner(t *testing.T, r *Runner) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := r.Wait(ctx); err != nil {
		t.Fatal(err)
	}
}
func TestRunnerStopWhileStarting(t *testing.T) {
	state := NewAppState(DefaultAppConfig())
	r := NewRunner(t.TempDir(), state)
	entered, release, completed := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	h := newFakeHandle()
	var opens atomic.Int32
	r.open = func() (packetHandle, error) { opens.Add(1); close(entered); <-release; return h, nil }
	go func() { completed <- r.Start(DefaultAppConfig()) }()
	<-entered
	if state.Snapshot().Status != StatusStarting {
		t.Fatal("expected starting")
	}
	if err := r.Stop(); err != nil {
		t.Fatal(err)
	}
	if state.Snapshot().Status != StatusStopping {
		t.Fatal("expected stopping until initialization finishes")
	}
	if err := r.Start(DefaultAppConfig()); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-completed; err != nil {
		t.Fatal(err)
	}
	waitRunner(t, r)
	if state.Snapshot().Status != StatusIdle || h.recvCount.Load() != 0 || h.closeCount.Load() != 1 || opens.Load() != 1 {
		t.Fatalf("cancelled initialization became active or leaked handle: status=%s recv=%d close=%d opens=%d", state.Snapshot().Status, h.recvCount.Load(), h.closeCount.Load(), opens.Load())
	}
}
func TestRunnerRepeatedStartStop(t *testing.T) {
	state := NewAppState(DefaultAppConfig())
	r := NewRunner(t.TempDir(), state)
	for iteration := 0; iteration < 10; iteration++ {
		h := newFakeHandle()
		r.open = func() (packetHandle, error) { return h, nil }
		if err := r.Start(DefaultAppConfig()); err != nil {
			t.Fatal(err)
		}
		<-h.recving
		var wg sync.WaitGroup
		for i := 0; i < 20; i++ {
			wg.Add(1)
			go func() { defer wg.Done(); _ = r.Stop() }()
		}
		wg.Wait()
		waitRunner(t, r)
		if h.closeCount.Load() != 1 || state.Snapshot().Status != StatusIdle {
			t.Fatalf("invalid stop: close=%d status=%s", h.closeCount.Load(), state.Snapshot().Status)
		}
	}
}
func TestRunnerFailureAndRestart(t *testing.T) {
	state := NewAppState(DefaultAppConfig())
	r := NewRunner(t.TempDir(), state)
	r.open = func() (packetHandle, error) { return nil, errors.New("driver unavailable") }
	if err := r.Start(DefaultAppConfig()); err == nil {
		t.Fatal("start error lost")
	}
	if state.Snapshot().Status != StatusError {
		t.Fatal("expected error state")
	}
	for _, panics := range []bool{false, true} {
		h := newFakeHandle()
		h.panicRecv = panics
		h.recvErr = errors.New("receive failed")
		r.open = func() (packetHandle, error) { return h, nil }
		if err := r.Start(DefaultAppConfig()); err != nil {
			t.Fatal(err)
		}
		waitRunner(t, r)
		if h.closeCount.Load() != 1 || state.Snapshot().Status != StatusError {
			t.Fatalf("failure not cleaned up: %+v", state.Snapshot())
		}
	}
}
func TestRunnerWaitTimesOutDuringInitialization(t *testing.T) {
	state := NewAppState(DefaultAppConfig())
	r := NewRunner(t.TempDir(), state)
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	r.open = func() (packetHandle, error) { close(entered); <-release; return newFakeHandle(), nil }
	go func() { defer close(done); _ = r.Start(DefaultAppConfig()) }()
	<-entered
	_ = r.Stop()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if !errors.Is(r.Wait(ctx), context.Canceled) {
		t.Fatal("Wait should honor context")
	}
	close(release)
	<-done
	waitRunner(t, r)
}

type failingCloseHandle struct {
	*fakePacketHandle
	attempts atomic.Int32
}

func (h *failingCloseHandle) Close() error {
	if h.attempts.Add(1) == 1 {
		return errors.New("synthetic close failure")
	}
	return h.fakePacketHandle.Close()
}
func TestRunnerCloseFailureCanBeRetriedWithoutOpeningAnotherHandle(t *testing.T) {
	state := NewAppState(DefaultAppConfig())
	r := NewRunner(t.TempDir(), state)
	h := &failingCloseHandle{fakePacketHandle: newFakeHandle()}
	h.recvErr = errors.New("receive failed")
	var opens atomic.Int32
	r.open = func() (packetHandle, error) { opens.Add(1); return h, nil }
	if err := r.Start(DefaultAppConfig()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := r.Wait(ctx); err == nil {
		t.Fatal("close failure was hidden")
	}
	if err := r.Start(DefaultAppConfig()); err == nil {
		t.Fatal("new start should report pending close error")
	}
	if opens.Load() != 1 {
		t.Fatal("new handle opened before old one closed")
	}
	if err := r.Stop(); err != nil {
		t.Fatal(err)
	}
	waitRunner(t, r)
	if h.closeCount.Load() != 1 || state.Snapshot().Status != StatusIdle {
		t.Fatal("failed close not recovered")
	}
}

type blockedFailingCloseHandle struct {
	*fakePacketHandle
	entered  chan struct{}
	release  chan struct{}
	attempts atomic.Int32
}

func (h *blockedFailingCloseHandle) Close() error {
	if h.attempts.Add(1) == 1 {
		close(h.entered)
		<-h.release
		return errors.New("synthetic first close failure")
	}
	return h.fakePacketHandle.Close()
}
func TestRunnerConcurrentCloseRetryFinishesSession(t *testing.T) {
	for i := 0; i < 50; i++ {
		state := NewAppState(DefaultAppConfig())
		r := NewRunner(t.TempDir(), state)
		h := &blockedFailingCloseHandle{fakePacketHandle: newFakeHandle(), entered: make(chan struct{}), release: make(chan struct{})}
		h.recvErr = errors.New("receive failed")
		r.open = func() (packetHandle, error) { return h, nil }
		if err := r.Start(DefaultAppConfig()); err != nil {
			t.Fatal(err)
		}
		<-h.entered
		stopped := make(chan error, 1)
		go func() { stopped <- r.Stop() }()
		close(h.release)
		if err := <-stopped; err != nil {
			t.Fatal(err)
		}
		waitRunner(t, r)
		if state.Snapshot().Status != StatusIdle {
			t.Fatalf("cleanup remained in %s after successful retry", state.Snapshot().Status)
		}
	}
}
