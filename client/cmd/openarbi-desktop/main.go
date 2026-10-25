// openarbi-desktop owns the per-user broker. Kitty integration consumes its
// command JSON; terminal attachments keep the existing native Codex interface.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/hustLer2k/openarbi-kitty/client/internal/client"
	"github.com/hustLer2k/openarbi-kitty/client/internal/desktop"
	"github.com/hustLer2k/openarbi-kitty/client/internal/terminaltext"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "OpenArbi:", terminaltext.Sanitize(err.Error()))
		os.Exit(1)
	}
}
func run() error {
	flags := flag.NewFlagSet("openarbi-desktop", flag.ContinueOnError)
	directory := flags.String("directory", "", "private desktop state directory")
	server := flags.String("server", "", "credential API origin")
	if err := flags.Parse(os.Args[1:]); err != nil {
		return err
	}
	if *directory == "" {
		var err error
		*directory, err = desktop.UserDirectory()
		if err != nil {
			return err
		}
	}
	if err := desktop.PrivateDirectory(*directory); err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	action := "launch"
	if flags.NArg() > 0 {
		action = flags.Arg(0)
	}
	if action == "shutdown" {
		if flags.NArg() != 1 {
			return errors.New("shutdown takes no arguments")
		}
		shutdownCtx, shutdownCancel := context.WithTimeout(ctx, 10*time.Second)
		defer shutdownCancel()
		return desktop.Shutdown(shutdownCtx, *directory)
	}
	prefs, err := client.UserPreferences()
	if err != nil {
		return err
	}
	origin, err := client.ResolveServer(*server, os.Getenv("OPENARBI_URL"), prefs)
	if err != nil {
		return err
	}
	switch action {
	case "serve":
		if flags.NArg() != 1 {
			return errors.New("serve takes no arguments")
		}
		return serve(ctx, *directory, origin)
	case "ensure":
		if flags.NArg() != 1 {
			return errors.New("ensure takes no arguments")
		}
		return ensure(ctx, *directory, origin)
	case "attach":
		if flags.NArg() != 2 {
			return errors.New("usage: openarbi-desktop attach TAB_ID")
		}
		return attach(ctx, *directory, origin, flags.Arg(1))
	case "launch", "status", "prepare", "command":
	default:
		return errors.New("unknown desktop command")
	}
	command := desktop.Command{Action: action}
	switch action {
	case "launch":
		if flags.NArg() > 1 {
			return errors.New("launch takes no arguments")
		}
		if err = ensure(ctx, *directory, origin); err != nil {
			return err
		}
	case "status":
		if flags.NArg() != 1 {
			return errors.New("status takes no arguments")
		}
	case "prepare":
		if flags.NArg() != 2 {
			return errors.New("prepare needs a tab ID")
		}
		command.TabID = flags.Arg(1)
	case "command":
		if flags.NArg() != 1 {
			return errors.New("command reads one JSON object from stdin")
		}
		decoder := json.NewDecoder(io.LimitReader(os.Stdin, 64<<10))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&command) != nil || decoder.Decode(new(any)) != io.EOF {
			return errors.New("invalid desktop command JSON")
		}
	}
	result, err := desktop.Send(ctx, *directory, command)
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(result)
}
func serve(ctx context.Context, directory, origin string) error {
	owner, err := desktop.Acquire(directory)
	if errors.Is(err, desktop.ErrBrokerRunning) {
		return nil
	}
	if err != nil {
		return err
	}
	defer owner.Close()
	tunnel := desktop.NewTunnel(ctx, directory)
	defer tunnel.Close()
	manager, err := desktop.NewManager(desktop.FileStore{Directory: directory}, desktop.Backend{Tunnel: tunnel, Origin: origin}, origin)
	if err != nil {
		return err
	}
	stopRequested := make(chan struct{})
	var stopOnce sync.Once
	handler := desktop.HandlerWithShutdown(manager, tunnel.Proxy(), func() error {
		if tunnel.ActiveConnections() != 0 {
			return errors.New("close OpenArbi native attachments before updating")
		}
		if err := manager.BeginShutdown(); err != nil {
			return err
		}
		stopOnce.Do(func() { close(stopRequested) })
		return nil
	})
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second, BaseContext: func(net.Listener) context.Context { return ctx }}
	done := make(chan error, 1)
	go func() { done <- server.Serve(owner.Listener) }()
	select {
	case err = <-done:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	case <-ctx.Done():
	case <-stopRequested:
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err = server.Shutdown(shutdownCtx)
	if err != nil {
		_ = server.Close()
	}
	serveErr := <-done
	if err != nil {
		return err
	}
	if !errors.Is(serveErr, http.ErrServerClosed) {
		return serveErr
	}
	return nil
}
func ensure(ctx context.Context, directory, origin string) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	check := func() bool {
		result, err := desktop.Send(ctx, directory, desktop.Command{Action: "status"})
		return err == nil && result.Workspace.Origin == origin
	}
	if check() {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// All contenders may spawn; flock makes exactly one process the owner. Losing
	// processes exit without touching the winning process's listener or state.
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	command := exec.Command(executable, "--directory", directory, "--server", origin, "serve")
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err = command.Start(); err != nil {
		return errors.New("cannot start desktop broker")
	}
	if err = command.Process.Release(); err != nil {
		return err
	}
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	poll := time.NewTicker(30 * time.Millisecond)
	defer poll.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return errors.New("desktop broker failed to start; run openarbi-desktop serve for its diagnostic")
		case <-poll.C:
			if check() {
				return nil
			}
		}
	}
}
func attach(ctx context.Context, directory, origin, tab string) error {
	fmt.Fprintln(os.Stderr, "Connecting your workspace…")
	for {
		result, err := desktop.Send(ctx, directory, desktop.Command{Action: "prepare", TabID: tab})
		if err != nil {
			return err
		}
		switch result.Status {
		case "ready":
			binary := os.Getenv("OPENARBI_CLIENT_BIN")
			if binary == "" {
				exe, err := os.Executable()
				if err != nil {
					return err
				}
				exe, err = filepath.EvalSymlinks(exe)
				if err != nil {
					return err
				}
				binary = filepath.Join(filepath.Dir(exe), "openarbi-client")
			}
			if result.Workspace.Origin != origin {
				return errors.New("workspace API origin changed; attachment stopped")
			}
			// The terminal CLI retains credential loading, pinned native Codex checks,
			// gateway host-key validation, and its complete attachment lifecycle.
			binary, err = exec.LookPath(binary)
			if err != nil {
				return errors.New("native OpenArbi client is unavailable")
			}
			// Hand off the foreground process. SIGTERM reaches the client's own
			// cancellation and joined Codex cleanup instead of killing its parent.
			return syscall.Exec(binary, []string{binary, "--server", origin, "--desktop-socket", filepath.Join(directory, "broker.sock"), "attach", result.RoomID}, os.Environ())
		case "preparing":
			fmt.Fprintln(os.Stderr, "Preparing your workspace…")
		case "waking":
			fmt.Fprintln(os.Stderr, "Waking your workspace…")
		case "waiting-for-storage":
			fmt.Fprintln(os.Stderr, "Waiting for workspace storage; retained workspaces will not be deleted…")
		case "stopped":
			return errors.New("workspace is stopped; use the room picker to deliberately resume it")
		case "recovery-required":
			return errors.New("workspace needs explicit recovery; open the room picker to inspect it before resuming")
		case "local":
			return errors.New("local tabs must be launched with the bundled shell integration")
		default:
			return errors.New("unknown workspace readiness state")
		}
		timer := time.NewTimer(time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}
