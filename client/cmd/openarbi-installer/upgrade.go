package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

func desktopDirectory() (string, error) {
	if value := os.Getenv("OPENARBI_DESKTOP_DIR"); value != "" {
		if !filepath.IsAbs(value) {
			return "", errors.New("OPENARBI_DESKTOP_DIR must be absolute")
		}
		return value, nil
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "OpenArbi/desktop"), nil
}
func upgradeLock(directory string) (*os.File, error) {
	if err := os.MkdirAll(directory, 0700); err != nil {
		return nil, err
	}
	fd, err := syscall.Open(filepath.Join(directory, "gui.lock"), syscall.O_CREAT|syscall.O_RDWR|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0600)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), "gui.lock")
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || stat.Uid != uint32(os.Getuid()) || info.Mode().Perm()&0077 != 0 {
		f.Close()
		return nil, errors.New("GUI lock is not a private owned regular file")
	}
	if err = syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, errors.New("OpenArbi is launching; retry the update when startup completes")
	}
	return f, nil
}
func brokerCommand(ctx context.Context, stage, directory, action string) error {
	cmd := exec.CommandContext(ctx, filepath.Join(stage, "bin/openarbi-desktop"), "--directory", directory, action)
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("desktop %s: %w", action, err)
	}
	return nil
}
func restartBroker(stage, directory string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return brokerCommand(ctx, stage, directory, "ensure")
}

// reconcileGUI holds gui.lock in its caller. A missing/refused owned socket proves
// that no Kitty server owns the previous live-window snapshot; all other unknown
// states fail closed before shutdown.
func reconcileGUI(ctx context.Context, stage, directory string, syncBroker bool) error {
	socket := filepath.Join(directory, "kitty.sock")
	info, err := os.Lstat(socket)
	absent := errors.Is(err, os.ErrNotExist)
	if err != nil && !absent {
		return err
	}
	if !absent {
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || info.Mode()&os.ModeSocket == 0 || stat.Uid != uint32(os.Getuid()) {
			return errors.New("Kitty socket ownership is ambiguous; close OpenArbi and inspect the socket before updating")
		}
		connection, e := (&net.Dialer{Timeout: time.Second}).DialContext(ctx, "unix", socket)
		if e != nil {
			if !errors.Is(e, syscall.ECONNREFUSED) {
				return errors.New("Kitty connection state is ambiguous; close OpenArbi and retry")
			}
			absent = true
		} else {
			connection.Close()
		}
	}
	if !absent {
		output, err := exec.CommandContext(ctx, filepath.Join(stage, "bin/kitty"), "@", "--to", "unix:"+socket, "ls", "--all-env-vars").Output()
		if err != nil {
			return errors.New("could not inspect Kitty windows; close OpenArbi and retry")
		}
		var windows []json.RawMessage
		if err = json.Unmarshal(output, &windows); err != nil {
			return errors.New("invalid Kitty window response; update cancelled")
		}
		if len(windows) > 0 {
			return errors.New("close all OpenArbi windows before updating")
		}
	}
	if !syncBroker {
		return nil
	}
	cmd := exec.CommandContext(ctx, filepath.Join(stage, "bin/openarbi-desktop"), "--directory", directory, "command")
	cmd.Stdin = strings.NewReader(`{"action":"sync-windows","order":[]}`)
	cmd.Stderr = os.Stderr
	if err = cmd.Run(); err != nil {
		return fmt.Errorf("could not reconcile closed desktop windows: %w", err)
	}
	return nil
}
