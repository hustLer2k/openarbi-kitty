package client

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestServerPriorityFlagEnvironmentSavedDefault(t *testing.T) {
	p := Preferences{Path: filepath.Join(t.TempDir(), "client.json")}
	if got, err := ResolveServer("", "", p); err != nil || got != initialServer {
		t.Fatal(got, err)
	}
	if err := p.SaveServer("https://stored.invalid/"); err != nil {
		t.Fatal(err)
	}
	for _, entry := range []struct{ flag, env, want string }{{"https://flag.invalid", "https://environment.invalid", "https://flag.invalid"}, {"", "https://environment.invalid", "https://environment.invalid"}, {"", "", "https://stored.invalid"}} {
		got, err := ResolveServer(entry.flag, entry.env, p)
		if err != nil || got != entry.want {
			t.Fatalf("%+v => %s %v", entry, got, err)
		}
	}
}
func TestInvalidPreferencesAreExplicitAndOverridesRepair(t *testing.T) {
	p := Preferences{Path: filepath.Join(t.TempDir(), "client.json")}
	if err := os.WriteFile(p.Path, []byte(`{"server":"http://remote.invalid","token":"must-not-be-used"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveServer("", "", p); err == nil {
		t.Fatal("invalid preferences silently switched server")
	}
	if got, err := ResolveServer("https://explicit.invalid", "", p); err != nil || got != "https://explicit.invalid" {
		t.Fatal(got, err)
	}
	if err := p.SaveServer("https://repaired.invalid"); err != nil {
		t.Fatal(err)
	}
	if got, err := p.Server(); err != nil || got != "https://repaired.invalid" {
		t.Fatal(got, err)
	}
}
func TestPreferencesAtomicModeAndNoCredentials(t *testing.T) {
	p := Preferences{Path: filepath.Join(t.TempDir(), "OpenArbi", "client.json")}
	if err := p.SaveServer("https://api.invalid"); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(p.Path)
	if err := p.SaveServer("https://secret:password@api.invalid"); err == nil {
		t.Fatal("credential URL saved")
	}
	after, _ := os.ReadFile(p.Path)
	if string(before) != string(after) {
		t.Fatal("invalid write replaced preference")
	}
	stat, err := os.Stat(p.Path)
	if err != nil || stat.Mode().Perm() != 0600 {
		t.Fatal("preference mode not0600")
	}
	var stored map[string]any
	if json.Unmarshal(after, &stored) != nil || len(stored) != 1 || stored["server"] != "https://api.invalid" {
		t.Fatal("unexpected preference fields")
	}
	entries, _ := os.ReadDir(filepath.Dir(p.Path))
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".client-") {
			t.Fatal("temporary preference file left behind")
		}
	}
}
