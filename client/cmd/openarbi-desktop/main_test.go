package main

import (
	"context"
	"github.com/hustLer2k/openarbi-kitty/client/internal/desktop"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestEnsureBoundsFirstProbeWithCallerDeadline(t *testing.T) {
	// t.TempDir includes the test name, exceeding Darwin's Unix socket limit.
	directory, err := os.MkdirTemp("", "oa-probe-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(directory); err != nil {
			t.Error(err)
		}
	})
	if err := os.Chmod(directory, 0700); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", filepath.Join(directory, "broker.sock"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(directory, "broker.sock"), 0600); err != nil {
		t.Fatal(err)
	}
	var entered atomic.Bool
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		entered.Store(true)
		<-r.Context().Done()
	})}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close(); <-done })
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	if err := ensure(ctx, directory, "http://127.0.0.1:8090"); err == nil {
		t.Fatal("unresponsive broker accepted")
	}
	if time.Since(started) > time.Second {
		t.Fatal("initial probe escaped startup deadline")
	}
	if !entered.Load() {
		t.Fatal("deadline test did not reach the blocking broker handler")
	}
}

func TestShutdownCommandPreservesWorkspaceAndRefusesActiveWindows(t *testing.T) {
	dir, err := os.MkdirTemp("", "oa-shutdown-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	if err = desktop.Shutdown(t.Context(), dir); err != nil {
		t.Fatal("absent shutdown:", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- serve(ctx, dir, "http://127.0.0.1:8090") }()
	deadline := time.After(2 * time.Second)
	for {
		if _, err = desktop.Send(t.Context(), dir, desktop.Command{Action: "status"}); err == nil {
			break
		}
		select {
		case <-deadline:
			t.Fatal("broker not ready")
		case <-time.After(10 * time.Millisecond):
		}
	}
	launch, err := desktop.Send(t.Context(), dir, desktop.Command{Action: "launch"})
	if err != nil {
		t.Fatal(err)
	}
	if err = desktop.Shutdown(t.Context(), dir); err == nil {
		t.Fatal("shutdown interrupted active window")
	}
	if _, err = desktop.Send(t.Context(), dir, desktop.Command{Action: "close-window", WindowID: launch.Windows[0]}); err != nil {
		t.Fatal(err)
	}
	shutdownCtx, shutdownCancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer shutdownCancel()
	if err = desktop.Shutdown(shutdownCtx, dir); err != nil {
		t.Fatal(err)
	}
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	running, err := desktop.BrokerRunning(dir)
	if err != nil || running {
		t.Fatal("shutdown returned before ownership release")
	}
	state, err := (desktop.FileStore{Directory: dir}).Load()
	if err != nil || len(state.Windows) != 1 || state.Windows[0].ID != launch.Windows[0] {
		t.Fatal("shutdown removed saved workspace")
	}
	if err = desktop.Shutdown(t.Context(), dir); err != nil {
		t.Fatal("repeated shutdown:", err)
	}
}
