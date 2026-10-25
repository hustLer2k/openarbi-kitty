package desktop

import (
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestConcurrentBrokerOwnership(t *testing.T) {
	dir := privateTemp(t)
	var wg sync.WaitGroup
	results := make(chan *Ownership, 20)
	errs := make(chan error, 20)
	for range 20 {
		wg.Go(func() {
			owner, err := Acquire(dir)
			if owner != nil {
				results <- owner
			}
			if err != nil {
				errs <- err
			}
		})
	}
	wg.Wait()
	close(results)
	close(errs)
	owners := 0
	for owner := range results {
		owners++
		defer owner.Close()
	}
	if owners != 1 {
		t.Fatalf("got %d broker owners", owners)
	}
	for err := range errs {
		if !errors.Is(err, ErrBrokerRunning) {
			t.Fatal(err)
		}
	}
	st, err := os.Stat(filepath.Join(dir, "broker.sock"))
	if err != nil || st.Mode().Perm() != 0600 {
		t.Fatal("socket is not private")
	}
}
func TestStaleSocketRecoveredOnlyByOwner(t *testing.T) {
	dir := privateTemp(t)
	path := filepath.Join(dir, "broker.sock")
	stale, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	stale.SetUnlinkOnClose(false)
	stale.Close()
	owner, err := Acquire(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = Acquire(dir); !errors.Is(err, ErrBrokerRunning) {
		t.Fatalf("second owner: %v", err)
	}
	owner.Close()
	next, err := Acquire(dir)
	if err != nil {
		t.Fatal(err)
	}
	next.Close()
}
func TestUnsafePathsAreRejected(t *testing.T) {
	dir := privateTemp(t)
	if err := os.Chmod(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := Acquire(dir); err == nil {
		t.Fatal("public directory accepted")
	}
	os.Chmod(dir, 0700)
	if err := os.WriteFile(filepath.Join(dir, "broker.sock"), []byte("unrelated"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Acquire(dir); err == nil {
		t.Fatal("unrelated file removed")
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "broker.sock"))
	if string(raw) != "unrelated" {
		t.Fatal("unrelated file changed")
	}
}
func TestBrokerCommandRoundTrip(t *testing.T) {
	dir := privateTemp(t)
	owner, err := Acquire(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	manager, _, _ := setup(t)
	server := &http.Server{Handler: Handler(manager, nil)}
	done := make(chan error, 1)
	go func() { done <- server.Serve(owner.Listener) }()
	defer func() { server.Close(); <-done }()
	first, err := Send(t.Context(), dir, Command{Action: "launch"})
	if err != nil || len(first.Windows) != 1 {
		t.Fatalf("launch: %v", err)
	}
	second, err := Send(t.Context(), dir, Command{Action: "launch"})
	if err != nil || second.Windows[0] == first.Windows[0] {
		t.Fatalf("second launch: %v", err)
	}
	if _, err = Send(t.Context(), dir, Command{Action: "unknown"}); err == nil {
		t.Fatal("unknown action accepted")
	}
}
