package main

import (
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestUpgradeLockExcludesParallelLaunch(t *testing.T) {
	dir := t.TempDir()
	first, err := upgradeLock(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	if second, err := upgradeLock(dir); err == nil {
		second.Close()
		t.Fatal("parallel update lock acquired")
	}
}
func TestUpgradeLockRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(t.TempDir(), "other")
	os.WriteFile(file, []byte("keep"), 0600)
	os.Symlink(file, filepath.Join(dir, "gui.lock"))
	if f, err := upgradeLock(dir); err == nil {
		f.Close()
		t.Fatal("symlink lock accepted")
	}
}
func TestReconcileMissingRefusedAndForeignSockets(t *testing.T) {
	dir := t.TempDir()
	if err := reconcileGUI(t.Context(), "unused", dir, false); err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(dir, "kitty.sock")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: socket, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	listener.SetUnlinkOnClose(false)
	listener.Close()
	if err = reconcileGUI(t.Context(), "unused", dir, false); err != nil {
		t.Fatal("refused stale socket should allow update", err)
	}
	os.Remove(socket)
	os.WriteFile(socket, []byte("unrelated"), 0600)
	if err = reconcileGUI(t.Context(), "unused", dir, false); err == nil {
		t.Fatal("foreign file accepted as inactive socket")
	}
}
func TestReconcileRefusesLiveKittyWindows(t *testing.T) {
	dir := t.TempDir()
	socket := filepath.Join(dir, "kitty.sock")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: socket, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	stage := t.TempDir()
	os.MkdirAll(filepath.Join(stage, "bin"), 0700)
	os.WriteFile(filepath.Join(stage, "bin/kitty"), []byte("#!/bin/sh\nprintf '[{\"id\":1}]'\n"), 0700)
	if err = reconcileGUI(t.Context(), stage, dir, false); err == nil {
		t.Fatal("live GUI accepted for update")
	}
}

func TestUpgradeLockIsNotInheritedByNewBroker(t *testing.T) {
	dir := t.TempDir()
	held, err := upgradeLock(dir)
	if err != nil {
		t.Fatal(err)
	}
	child := exec.Command("sleep", "30")
	if err = child.Start(); err != nil {
		held.Close()
		t.Fatal(err)
	}
	defer func() { _ = child.Process.Kill(); _ = child.Wait() }()
	if err = held.Close(); err != nil {
		t.Fatal(err)
	}
	next, err := upgradeLock(dir)
	if err != nil {
		t.Fatal("child inherited GUI update lock", err)
	}
	next.Close()
}
