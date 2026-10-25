package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/hustLer2k/openarbi-kitty/client/internal/install"
)

func TestUnavailableAudioIsReported(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	if err := playSound(t.Context(), filepath.Join(t.TempDir(), "ready.wav")); err == nil {
		t.Fatal("missing audio player should return a nonfatal error to caller")
	}
}
func TestDesktopEntryPreservesUnrelatedAndQuotesPaths(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux desktop entry")
	}
	home := filepath.Join(t.TempDir(), "home with spaces")
	t.Setenv("HOME", home)
	o := install.Options{Bin: filepath.Join(home, "bin/openarbi")}
	if err := desktopEntry(o); err != nil {
		t.Fatal(err)
	}
	entry := filepath.Join(home, ".local/share/applications/openarbi.desktop")
	data, err := os.ReadFile(entry)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `Exec="`+o.Bin+`"`) {
		t.Fatalf("launcher path not quoted: %s", data)
	}
	if err = os.WriteFile(entry, []byte("unrelated app"), 0644); err != nil {
		t.Fatal(err)
	}
	if err = desktopEntry(o); err == nil {
		t.Fatal("unrelated shortcut accepted")
	}
	data, err = os.ReadFile(entry)
	if err != nil || string(data) != "unrelated app" {
		t.Fatal("unrelated shortcut overwritten")
	}
}
