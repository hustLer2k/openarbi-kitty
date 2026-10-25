package main

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestLegacyLinuxTunnelRetirementAndRollback(t *testing.T) {
	home := t.TempDir()
	config := filepath.Join(home, ".config")
	root := filepath.Join(home, "install")
	unit := filepath.Join(config, "systemd/user", legacyUnit)
	os.MkdirAll(filepath.Dir(unit), 0700)
	original := legacyUnitText("/usr/bin/ssh")
	os.WriteFile(unit, []byte(original), 0600)
	var calls []string
	run := func(_ context.Context, command string, args ...string) ([]byte, error) {
		calls = append(calls, command+" "+strings.Join(args, " "))
		if len(args) > 1 && args[1] == "show" {
			return []byte("FragmentPath=" + unit + "\nDropInPaths=\nMainPID=0\nActiveState=inactive\nUnitFileState=enabled\n"), nil
		}
		return nil, nil
	}
	undo, err := retireLegacyTunnel(t.Context(), home, config, root, "linux", run)
	if err != nil {
		t.Fatal(err)
	}
	if undo == nil {
		t.Fatal("missing rollback")
	}
	if _, err = os.Stat(unit); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("old service file retained")
	}
	if err = undo(); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(unit)
	if string(raw) != original {
		t.Fatal("rollback did not restore definition")
	}
	joined := strings.Join(calls, "\n")
	if !strings.Contains(joined, "disable --now "+legacyUnit) || !strings.Contains(joined, "enable "+legacyUnit) {
		t.Fatal("service lifecycle missing")
	}
}
func TestLegacyTunnelRefusesUnknownDefinitions(t *testing.T) {
	home := t.TempDir()
	config := filepath.Join(home, ".config")
	unit := filepath.Join(config, "systemd/user", legacyUnit)
	os.MkdirAll(filepath.Dir(unit), 0700)
	os.WriteFile(unit, []byte(legacyUnitText("/usr/bin/ssh")+"# altered\n"), 0600)
	called := false
	run := func(context.Context, string, ...string) ([]byte, error) { called = true; return nil, nil }
	if _, err := retireLegacyTunnel(t.Context(), home, config, filepath.Join(home, "install"), "linux", run); err == nil {
		t.Fatal("unknown service retired")
	}
	if called {
		t.Fatal("supervisor touched before ownership verification")
	}
	if _, err := os.Stat(unit); err != nil {
		t.Fatal("unknown service removed")
	}
}
func TestLegacyTunnelRefusesLoadedOverrides(t *testing.T) {
	home := t.TempDir()
	config := filepath.Join(home, ".config")
	unit := filepath.Join(config, "systemd/user", legacyUnit)
	os.MkdirAll(filepath.Dir(unit), 0700)
	os.WriteFile(unit, []byte(legacyUnitText("/usr/bin/ssh")), 0600)
	calls := 0
	run := func(context.Context, string, ...string) ([]byte, error) {
		calls++
		return []byte("FragmentPath=" + unit + "\nDropInPaths=/other/override\nMainPID=0\nActiveState=inactive\n"), nil
	}
	if _, err := retireLegacyTunnel(t.Context(), home, config, filepath.Join(home, "install"), "linux", run); err == nil {
		t.Fatal("overridden service retired")
	}
	if calls != 1 {
		t.Fatal("service was mutated")
	}
}
func plistFixture(home string) []byte {
	return plistFixtureArgs(home, legacySSH("/usr/bin/ssh"))
}
func plistFixtureArgs(home string, argv []string) []byte {
	esc := func(s string) string { var b strings.Builder; xml.EscapeText(&b, []byte(s)); return b.String() }
	var args strings.Builder
	for _, arg := range argv {
		fmt.Fprintf(&args, "<string>%s</string>", esc(arg))
	}
	return []byte(`<plist version="1.0"><dict><key>Label</key><string>` + legacyLabel + `</string><key>ProgramArguments</key><array>` + args.String() + `</array><key>RunAtLoad</key><true/><key>KeepAlive</key><true/><key>ThrottleInterval</key><integer>10</integer><key>ProcessType</key><string>Background</string><key>StandardOutPath</key><string>` + esc(filepath.Join(home, "Library/Logs/OpenArbi/revenge-tunnel.stdout.log")) + `</string><key>StandardErrorPath</key><string>` + esc(filepath.Join(home, "Library/Logs/OpenArbi/revenge-tunnel.stderr.log")) + `</string></dict></plist>`)
}
func TestLegacyMacPlistAndRetirement(t *testing.T) {
	home := t.TempDir()
	raw := plistFixture(home)
	if _, err := validateLegacyPlist(raw, home); err != nil {
		t.Fatal(err)
	}
	if _, err := validateLegacyPlist([]byte(strings.Replace(string(raw), "revenge</string>", "unrelated</string>", 1)), home); err == nil {
		t.Fatal("modified plist accepted")
	}
	file := filepath.Join(home, "Library/LaunchAgents", legacyLabel+".plist")
	os.MkdirAll(filepath.Dir(file), 0700)
	os.WriteFile(file, raw, 0600)
	var calls []string
	run := func(_ context.Context, name string, args ...string) ([]byte, error) {
		calls = append(calls, strings.Join(args, " "))
		if args[0] == "print" {
			return []byte("Could not find service"), errors.New("absent")
		}
		return nil, nil
	}
	undo, err := retireLegacyTunnel(t.Context(), home, "", filepath.Join(home, "install"), "darwin", run)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(calls, []string{fmt.Sprintf("print gui/%d/%s", os.Getuid(), legacyLabel)}) {
		t.Fatal("inactive service unnecessarily stopped")
	}
	if err = undo(); err != nil {
		t.Fatal(err)
	}
}

// This first-generation argv is a fixed historical fixture, independent of the
// current argument builder. No credentials or machine-specific paths are used.
func firstMacTunnelArgs() []string {
	return []string{"/usr/bin/ssh", "-N", "-T", "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=yes", "-o", "ExitOnForwardFailure=yes", "-o", "ServerAliveInterval=15", "-o", "ServerAliveCountMax=3", "-o", "ConnectTimeout=10", "-o", "ControlMaster=no", "-o", "ControlPath=none", "-L", "127.0.0.1:29090:127.0.0.1:29090", "-L", "127.0.0.1:29222:127.0.0.1:29222", "revenge"}
}

func TestLegacyMacHistoricalVariantsRequireMatchingLoadedArguments(t *testing.T) {
	variants := map[string][]string{"first": firstMacTunnelArgs(), "foreground": legacySSH("/usr/bin/ssh")}
	for fileVariant, fileArgs := range variants {
		for loadedVariant, loadedArgs := range variants {
			t.Run(fileVariant+"-loaded-"+loadedVariant, func(t *testing.T) {
				home := t.TempDir()
				file := filepath.Join(home, "Library/LaunchAgents", legacyLabel+".plist")
				if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
					t.Fatal(err)
				}
				raw := plistFixtureArgs(home, fileArgs)
				if err := os.WriteFile(file, raw, 0600); err != nil {
					t.Fatal(err)
				}
				var calls []string
				run := func(_ context.Context, command string, args ...string) ([]byte, error) {
					if command != "launchctl" {
						t.Fatalf("unexpected supervisor: %s", command)
					}
					calls = append(calls, args[0])
					if args[0] == "print" {
						return []byte("program = /usr/bin/ssh\narguments = {\n" + strings.Join(loadedArgs, "\n") + "\n}\n"), nil
					}
					return nil, nil
				}
				root := filepath.Join(home, "install")
				undo, err := retireLegacyTunnel(t.Context(), home, "", root, "darwin", run)
				if fileVariant != loadedVariant {
					if err == nil || undo != nil {
						t.Fatal("mismatched loaded definition accepted")
					}
					if !reflect.DeepEqual(calls, []string{"print"}) {
						t.Fatal("service mutated before match")
					}
					got, readErr := os.ReadFile(file)
					if readErr != nil || string(got) != string(raw) {
						t.Fatal("mismatched service definition changed")
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				if undo == nil {
					t.Fatal("missing rollback")
				}
				if _, err := os.Stat(file); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("managed definition retained")
				}
				backups, err := filepath.Glob(filepath.Join(root, "legacy-backups", "*"))
				if err != nil || len(backups) != 1 {
					t.Fatal("missing unique backup")
				}
				backup, err := os.ReadFile(backups[0])
				if err != nil || string(backup) != string(raw) {
					t.Fatal("backup changed historical definition")
				}
				if err := undo(); err != nil {
					t.Fatal(err)
				}
				restored, err := os.ReadFile(file)
				if err != nil || string(restored) != string(raw) {
					t.Fatal("rollback changed historical definition")
				}
				if !reflect.DeepEqual(calls, []string{"print", "bootout", "bootstrap"}) {
					t.Fatalf("unexpected lifecycle: %v", calls)
				}
			})
		}
	}
}

func TestLegacyMacRejectsNearMatchesWithoutSupervisorChanges(t *testing.T) {
	for variant, argv := range map[string][]string{"first": firstMacTunnelArgs(), "foreground": legacySSH("/usr/bin/ssh")} {
		t.Run(variant, func(t *testing.T) {
			home := t.TempDir()
			raw := string(plistFixtureArgs(home, argv))
			for mutation, changed := range map[string]string{
				"alias":         strings.Replace(raw, "revenge</string>", "unrelated</string>", 1),
				"target":        strings.Replace(raw, "127.0.0.1:29090:127.0.0.1:29090", "127.0.0.1:29090:other:29090", 1),
				"option":        strings.Replace(raw, "ControlMaster=no", "ControlMaster=auto", 1),
				"extra-key":     strings.Replace(raw, "</dict>", "<key>Disabled</key><true/></dict>", 1),
				"duplicate-key": strings.Replace(raw, "</dict>", "<key>KeepAlive</key><true/></dict>", 1),
			} {
				t.Run(mutation, func(t *testing.T) {
					file := filepath.Join(home, "Library/LaunchAgents", legacyLabel+".plist")
					if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(file, []byte(changed), 0600); err != nil {
						t.Fatal(err)
					}
					called := false
					run := func(context.Context, string, ...string) ([]byte, error) { called = true; return nil, nil }
					undo, err := retireLegacyTunnel(t.Context(), home, "", filepath.Join(home, "install"), "darwin", run)
					if err == nil || undo != nil || called {
						t.Fatal("unknown definition reached supervisor")
					}
					got, err := os.ReadFile(file)
					if err != nil || string(got) != changed {
						t.Fatal("unknown definition changed")
					}
				})
			}
		})
	}
}
