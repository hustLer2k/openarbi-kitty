package install

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func legacyFixture(t *testing.T, root string) string {
	t.Helper()
	release := filepath.Join(root, "releases/old-cli")
	entries := map[string]legacyEntry{}
	for _, name := range []string{"openarbi", "codex"} {
		path := filepath.Join(release, "bundle", name)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("old-"+name), 0755); err != nil {
			t.Fatal(err)
		}
		digest, err := fileDigest(path)
		if err != nil {
			t.Fatal(err)
		}
		entries["bundle/"+name] = legacyEntry{SHA256: digest, Mode: 0755}
	}
	receipt, _ := json.Marshal(legacyReceipt{Schema: 2, Source: "source-provenance", CodexVersion: "0.164.0", Platform: "Linux/x86_64", Entries: entries})
	if err := os.WriteFile(filepath.Join(release, "installation.json"), receipt, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "bin"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(release, filepath.Join(root, "current")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "current/bundle/openarbi"), filepath.Join(root, "bin/openarbi")); err != nil {
		t.Fatal(err)
	}
	return release
}
func TestLegacyCLIUpgradeAndUninstall(t *testing.T) {
	for _, external := range []bool{false, true} {
		t.Run(map[bool]string{false: "same-root", true: "mac-external-root"}[external], func(t *testing.T) {
			archive, sum := payload(t)
			o := options(t, sum)
			prefix := o.Root
			if external {
				prefix = filepath.Join(filepath.Dir(o.Root), "old CLI")
				o.LegacyRoot = prefix
			}
			old := legacyFixture(t, prefix)
			stage, err := InstallArchive(t.Context(), o, archive)
			if err != nil {
				t.Fatal(err)
			}
			oldCLI := filepath.Join(prefix, "bin/openarbi")
			resolved, err := filepath.EvalSymlinks(oldCLI)
			if err != nil || resolved != filepath.Join(stage, "bin/openarbi") {
				t.Fatalf("old CLI not migrated: %s %v", resolved, err)
			}
			if _, err = os.Stat(filepath.Join(old, "bundle/codex")); err != nil {
				t.Fatal("old retained release was removed")
			}
			if _, err = InstallArchive(t.Context(), o, archive); err != nil {
				t.Fatalf("repeat update failed: %v", err)
			}
			if err = Uninstall(o); err != nil {
				t.Fatal(err)
			}
			if _, err = os.Lstat(oldCLI); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("managed old CLI link retained")
			}
		})
	}
}
func TestLegacyChangedFilePreserved(t *testing.T) {
	archive, sum := payload(t)
	o := options(t, sum)
	old := legacyFixture(t, o.Root)
	name := filepath.Join(old, "bundle/openarbi")
	os.WriteFile(name, []byte("unrelated replacement"), 0755)
	if _, err := InstallArchive(t.Context(), o, archive); err == nil {
		t.Fatal("modified legacy CLI adopted")
	}
	raw, _ := os.ReadFile(name)
	if string(raw) != "unrelated replacement" {
		t.Fatal("modified CLI overwritten")
	}
}
func TestLegacyActivationFailureRestoresOldLinks(t *testing.T) {
	archive, sum := payload(t)
	o := options(t, sum)
	old := legacyFixture(t, o.Root)
	o.AfterActivate = func(context.Context, string) error { return errors.New("fail") }
	if _, err := InstallArchive(t.Context(), o, archive); err == nil {
		t.Fatal("failure accepted")
	}
	resolved, err := filepath.EvalSymlinks(filepath.Join(o.Root, "bin/openarbi"))
	if err != nil || resolved != filepath.Join(old, "bundle/openarbi") {
		t.Fatalf("legacy link not restored: %s %v", resolved, err)
	}
}

func TestLegacyLinksStayOldUntilActivation(t *testing.T) {
	for _, external := range []bool{false, true} {
		t.Run(map[bool]string{false: "same-root", true: "external"}[external], func(t *testing.T) {
			archive, sum := payload(t)
			o := options(t, sum)
			prefix := o.Root
			if external {
				prefix = filepath.Join(filepath.Dir(o.Root), "old")
				o.LegacyRoot = prefix
			}
			old := legacyFixture(t, prefix)
			o.BeforeActivate = func(context.Context, string, string) (func() error, error) {
				resolved, err := filepath.EvalSymlinks(filepath.Join(prefix, "bin/openarbi"))
				if err != nil || resolved != filepath.Join(old, "bundle/openarbi") {
					t.Fatalf("old CLI changed before activation: %s %v", resolved, err)
				}
				return nil, errors.New("activation cancelled")
			}
			if _, err := InstallArchive(t.Context(), o, archive); err == nil {
				t.Fatal("cancelled activation succeeded")
			}
		})
	}
}
func TestExternalMigrationJournalCompletesActivatedAndRestoresUnactivated(t *testing.T) {
	for _, activated := range []bool{false, true} {
		t.Run(map[bool]string{false: "before-activation", true: "after-activation"}[activated], func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "install")
			prefix := filepath.Join(t.TempDir(), "old")
			old := legacyFixture(t, prefix)
			stage := filepath.Join(root, "releases/new")
			os.MkdirAll(filepath.Join(stage, "bin"), 0700)
			os.WriteFile(filepath.Join(stage, "bin/openarbi"), []byte("new"), 0755)
			name := filepath.Join(prefix, "bin/openarbi")
			previous := filepath.Join(prefix, "current/bundle/openarbi")
			target := filepath.Join(root, "current/bin/openarbi")
			if err := writeMigrationJournal(root, stage, map[string]string{name: previous}, map[string]string{name: target}); err != nil {
				t.Fatal(err)
			}
			if activated {
				os.Symlink("releases/new", filepath.Join(root, "current"))
			}
			if err := recoverMigration(root); err != nil {
				t.Fatal(err)
			}
			got, err := filepath.EvalSymlinks(name)
			want := filepath.Join(old, "bundle/openarbi")
			if activated {
				want = filepath.Join(stage, "bin/openarbi")
			}
			if err != nil || got != want {
				t.Fatalf("wrong recovery target: %s %v", got, err)
			}
		})
	}
}
func TestReceiptFinalizationFailureDoesNotStartNewBroker(t *testing.T) {
	archive, sum := payload(t)
	o := options(t, sum)
	old, err := InstallArchive(t.Context(), o, archive)
	if err != nil {
		t.Fatal(err)
	}
	started := false
	o.Version = "v1.0.1"
	o.BeforeActivate = func(context.Context, string, string) (func() error, error) {
		file := filepath.Join(o.Root, ".managed-links.json")
		if err := os.Remove(file); err != nil {
			return nil, err
		}
		if err := os.Mkdir(file, 0700); err != nil {
			return nil, err
		}
		return func() error { return os.Remove(file) }, nil
	}
	o.AfterActivate = func(context.Context, string) error { started = true; return nil }
	if _, err = InstallArchive(t.Context(), o, archive); err == nil {
		t.Fatal("receipt write obstruction accepted")
	}
	if started {
		t.Fatal("broker started before fallible finalization")
	}
	current, err := filepath.EvalSymlinks(filepath.Join(o.Root, "current"))
	if err != nil || current != old {
		t.Fatal("old payload not restored")
	}
}
