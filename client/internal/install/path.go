package install

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

const pathStart = "# >>> OpenArbi managed PATH >>>"
const pathEnd = "# <<< OpenArbi managed PATH <<<"

type pathReceipt struct {
	Block string   `json:"block"`
	Files []string `json:"files"`
	// Older receipts lack creation provenance; their files are retained.
	Created []string `json:"created,omitempty"`
}

// ConfigurePATH adds one narrowly delimited POSIX block to Bash and Zsh startup
// files. Existing bytes outside that block survive installation and removal.
func ConfigurePATH(home, zdir, root, bin string) error {
	if !filepath.IsAbs(home) || !filepath.IsAbs(bin) {
		return errors.New("PATH setup requires absolute home and bin paths")
	}
	if zdir == "" {
		zdir = home
	}
	if !filepath.IsAbs(zdir) {
		return errors.New("ZDOTDIR must be an absolute path")
	}
	login := filepath.Join(home, ".profile")
	for _, name := range []string{".bash_profile", ".bash_login"} {
		if _, err := os.Lstat(filepath.Join(home, name)); err == nil {
			login = filepath.Join(home, name)
			break
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	files := []string{filepath.Join(home, ".bashrc"), login, filepath.Join(zdir, ".zshrc"), filepath.Join(zdir, ".zprofile")}
	quoted := "'" + strings.ReplaceAll(filepath.Dir(bin), "'", "'\"'\"'") + "'"
	block := "\n" + pathStart + "\ncase \":$PATH:\" in\n  *:" + quoted + ":*) ;;\n  *) export PATH=" + quoted + ":\"$PATH\" ;;\nesac\n" + pathEnd + "\n"
	receiptPath := filepath.Join(root, ".managed-path.json")
	var old pathReceipt
	if raw, err := os.ReadFile(receiptPath); err == nil {
		if err = json.Unmarshal(raw, &old); err != nil {
			return fmt.Errorf("read managed PATH receipt: %w", err)
		}
		if old.Block != block {
			return errors.New("managed PATH location changed; remove previous PATH setup first")
		}
		for _, name := range old.Files {
			found := false
			for _, f := range files {
				found = found || f == name
			}
			if !found {
				files = append(files, name)
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	var changes []fileChange
	var created []string
	for _, name := range files {
		before, mode, exists, err := readRegular(name)
		if err != nil {
			return err
		}
		if !exists || slices.Contains(old.Created, name) {
			created = append(created, name)
		}
		if strings.Contains(string(before), pathStart) || strings.Contains(string(before), pathEnd) {
			if strings.Count(string(before), block) != 1 {
				return fmt.Errorf("managed PATH block was edited in %s; preserve it and repair manually", name)
			}
			continue
		}
		changes = append(changes, fileChange{name: name, before: before, after: append(append([]byte{}, before...), []byte(block)...), mode: mode, existed: exists})
	}
	raw, err := json.Marshal(pathReceipt{Block: block, Files: files, Created: created})
	if err != nil {
		return err
	}
	before, mode, exists, err := readRegular(receiptPath)
	if err != nil {
		return err
	}
	changes = append(changes, fileChange{name: receiptPath, before: before, after: append(raw, '\n'), mode: mode, existed: exists})
	return applyFiles(changes)
}
func RemovePATH(root string) error {
	receipt := filepath.Join(root, ".managed-path.json")
	raw, receiptMode, exists, err := readRegular(receipt)
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}
	var r pathReceipt
	if err = json.Unmarshal(raw, &r); err != nil {
		return err
	}
	if !strings.Contains(r.Block, pathStart) || !strings.Contains(r.Block, pathEnd) {
		return errors.New("invalid PATH receipt")
	}
	var changes []fileChange
	for _, name := range r.Files {
		before, mode, exists, err := readRegular(name)
		if err != nil {
			return err
		}
		if !exists {
			continue
		}
		text := string(before)
		if !strings.Contains(text, pathStart) && !strings.Contains(text, pathEnd) {
			continue
		}
		if strings.Count(text, r.Block) != 1 {
			return fmt.Errorf("managed PATH block was edited in %s; refusing to remove user changes", name)
		}
		after := []byte(strings.Replace(text, r.Block, "", 1))
		changes = append(changes, fileChange{name: name, before: before, after: after, mode: mode, existed: true,
			remove: len(after) == 0 && slices.Contains(r.Created, name)})
	}
	// Receipt removal belongs to the same rollback transaction as shell files.
	changes = append(changes, fileChange{name: receipt, before: raw, mode: receiptMode, existed: true, remove: true})
	return applyFiles(changes)
}

type fileChange struct {
	name          string
	before, after []byte
	mode          os.FileMode
	existed       bool
	remove        bool
}

func readRegular(name string) ([]byte, os.FileMode, bool, error) {
	info, err := os.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil, 0644, false, nil
	}
	if err != nil {
		return nil, 0, false, err
	}
	if !info.Mode().IsRegular() {
		return nil, 0, false, fmt.Errorf("refusing to edit non-regular shell configuration %s", name)
	}
	data, err := os.ReadFile(name)
	return data, info.Mode().Perm(), true, err
}
func atomicFile(name string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(name), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(name), ".openarbi-write-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(mode); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(f.Name(), name)
}
func applyFiles(changes []fileChange) (result error) {
	var applied []fileChange
	defer func() {
		if result != nil {
			for i := len(applied) - 1; i >= 0; i-- {
				c := applied[i]
				var err error
				if c.existed {
					err = atomicFile(c.name, c.before, c.mode)
				} else {
					err = os.Remove(c.name)
				}
				result = errors.Join(result, err)
			}
		}
	}()
	for _, c := range changes {
		var err error
		if c.remove {
			err = os.Remove(c.name)
		} else {
			err = atomicFile(c.name, c.after, c.mode)
		}
		if err != nil {
			return err
		}
		applied = append(applied, c)
	}
	return nil
}
