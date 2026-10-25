package install

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestPATHInstallRemovePreservesBashZshFiles(t *testing.T) {
	for _, login := range []string{".profile", ".bash_profile", ".bash_login"} {
		t.Run(login, func(t *testing.T) {
			home := filepath.Join(t.TempDir(), "home ' spaces")
			zdir := filepath.Join(home, "custom zsh")
			root := filepath.Join(home, "install")
			bin := filepath.Join(home, "bin/openarbi")
			files := []string{filepath.Join(home, ".bashrc"), filepath.Join(home, login), filepath.Join(zdir, ".zshrc"), filepath.Join(zdir, ".zprofile")}
			original := "export ORIGINAL=keep\n# no trailing newline"
			for _, name := range files {
				if err := os.MkdirAll(filepath.Dir(name), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(name, []byte(original), 0600); err != nil {
					t.Fatal(err)
				}
			}
			for range 2 {
				if err := ConfigurePATH(home, zdir, root, bin); err != nil {
					t.Fatal(err)
				}
			}
			for _, name := range files {
				raw, err := os.ReadFile(name)
				if err != nil {
					t.Fatal(err)
				}
				if strings.Count(string(raw), pathStart) != 1 {
					t.Fatal("PATH block duplicated")
				}
				command := exec.Command("bash", "--noprofile", "--norc", "-c", `. "$1"; printf '%s' "$PATH"`, "test", name)
				command.Env = []string{"PATH=/usr/bin:/bin"}
				output, err := command.Output()
				if err != nil {
					t.Fatal(err)
				}
				if !strings.HasPrefix(string(output), filepath.Dir(bin)+":") {
					t.Fatalf("PATH not set: %s", output)
				}
			}
			// User edits outside the block must survive uninstall byte-for-byte.
			file := files[0]
			f, err := os.OpenFile(file, os.O_APPEND|os.O_WRONLY, 0)
			if err != nil {
				t.Fatal(err)
			}
			f.WriteString("# added after install\n")
			f.Close()
			if err := RemovePATH(root); err != nil {
				t.Fatal(err)
			}
			for i, name := range files {
				raw, err := os.ReadFile(name)
				if err != nil {
					t.Fatal(err)
				}
				want := original
				if i == 0 {
					want += "# added after install\n"
				}
				if string(raw) != want {
					t.Fatalf("unrelated shell configuration changed: %q", raw)
				}
			}
		})
	}
}
func TestPATHRejectsEditedBlocksAndSymlinks(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, "install")
	bin := filepath.Join(home, "bin/openarbi")
	if err := ConfigurePATH(home, "", root, bin); err != nil {
		t.Fatal(err)
	}
	name := filepath.Join(home, ".bashrc")
	raw, _ := os.ReadFile(name)
	raw = []byte(strings.Replace(string(raw), "export PATH=", "export EDITED_PATH=", 1))
	os.WriteFile(name, raw, 0644)
	if err := RemovePATH(root); err == nil {
		t.Fatal("edited block removed")
	}
	actual, _ := os.ReadFile(name)
	if string(actual) != string(raw) {
		t.Fatal("edited file changed")
	}
	other := t.TempDir()
	target := filepath.Join(other, "actual")
	os.WriteFile(target, []byte("unrelated"), 0600)
	os.Symlink(target, filepath.Join(other, ".bashrc"))
	if err := ConfigurePATH(other, "", filepath.Join(other, "install"), filepath.Join(other, "bin/openarbi")); err == nil {
		t.Fatal("symlink shell configuration followed")
	}
}

func TestPATHRemoveCreatedFilesAndPreserveUserContent(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, "install")
	bin := filepath.Join(home, "bin/openarbi")
	preexisting := filepath.Join(home, ".bashrc")
	if err := os.WriteFile(preexisting, nil, 0600); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := ConfigurePATH(home, "", root, bin); err != nil {
			t.Fatal(err)
		}
	}
	userFile := filepath.Join(home, ".zshrc")
	f, err := os.OpenFile(userFile, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("# user content\n"); err != nil {
		f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if err := ConfigurePATH(home, "", root, bin); err != nil {
		t.Fatal(err)
	}
	if err := RemovePATH(root); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{".profile", ".zprofile"} {
		if _, err := os.Lstat(filepath.Join(home, name)); !os.IsNotExist(err) {
			t.Fatalf("installer-created %s retained: %v", name, err)
		}
	}
	if raw, err := os.ReadFile(preexisting); err != nil || len(raw) != 0 {
		t.Fatalf("pre-existing empty file changed or removed: %q %v", raw, err)
	}
	if raw, err := os.ReadFile(userFile); err != nil || string(raw) != "# user content\n" {
		t.Fatalf("user additions changed or removed: %q %v", raw, err)
	}
	if _, err := os.Lstat(filepath.Join(root, ".managed-path.json")); !os.IsNotExist(err) {
		t.Fatalf("PATH receipt retained: %v", err)
	}
}

func TestPATHOldReceiptPreservesEmptyFiles(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, "install")
	bin := filepath.Join(home, "bin/openarbi")
	if err := ConfigurePATH(home, "", root, bin); err != nil {
		t.Fatal(err)
	}
	name := filepath.Join(root, ".managed-path.json")
	raw, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	var receipt map[string]json.RawMessage
	if err := json.Unmarshal(raw, &receipt); err != nil {
		t.Fatal(err)
	}
	delete(receipt, "created")
	raw, err = json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := ConfigurePATH(home, "", root, bin); err != nil {
		t.Fatal(err)
	}
	if err := RemovePATH(root); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{".bashrc", ".profile", ".zshrc", ".zprofile"} {
		if raw, err := os.ReadFile(filepath.Join(home, name)); err != nil || len(raw) != 0 {
			t.Fatalf("old receipt invented creation provenance for %s: %q %v", name, raw, err)
		}
	}
}

func TestPATHFileTransactionRestoresDeletionOnFailure(t *testing.T) {
	dir := t.TempDir()
	name := filepath.Join(dir, ".bashrc")
	before := []byte("managed content\n")
	if err := os.WriteFile(name, before, 0600); err != nil {
		t.Fatal(err)
	}
	obstruction := filepath.Join(dir, "obstruction")
	if err := os.Mkdir(obstruction, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(obstruction, "keep"), []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := applyFiles([]fileChange{
		{name: name, before: before, mode: 0600, existed: true, remove: true},
		{name: obstruction, existed: true, remove: true},
	}); err == nil {
		t.Fatal("failed deletion accepted")
	}
	if raw, err := os.ReadFile(name); err != nil || string(raw) != string(before) {
		t.Fatalf("deleted file was not restored: %q %v", raw, err)
	}
	if info, err := os.Stat(name); err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("restored file mode changed: %v %v", info, err)
	}
	if raw, err := os.ReadFile(filepath.Join(obstruction, "keep")); err != nil || string(raw) != "keep" {
		t.Fatalf("obstruction changed: %q %v", raw, err)
	}
}
