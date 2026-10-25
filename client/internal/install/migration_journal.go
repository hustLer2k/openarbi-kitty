package install

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

type pendingLink struct {
	Previous string `json:"previous"`
	Target   string `json:"target"`
}
type migrationJournal struct {
	Release string                 `json:"release"`
	Links   map[string]pendingLink `json:"links"`
}

func replaceSymlink(name, target string) error {
	f, err := os.CreateTemp(filepath.Dir(name), ".openarbi-link-")
	if err != nil {
		return err
	}
	tmp := f.Name()
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Remove(tmp); err != nil {
		return err
	}
	defer os.Remove(tmp)
	if err = os.Symlink(target, tmp); err != nil {
		return err
	}
	return os.Rename(tmp, name)
}
func writeMigrationJournal(root, stage string, previous, targets map[string]string) error {
	if len(previous) == 0 {
		return nil
	}
	j := migrationJournal{stage, map[string]pendingLink{}}
	for name, target := range previous {
		j.Links[name] = pendingLink{target, targets[name]}
	}
	raw, err := json.Marshal(j)
	if err != nil {
		return err
	}
	return atomicFile(filepath.Join(root, ".pending-links.json"), raw, 0600)
}

// recoverMigration completes only a journaled, already-activated external-link
// migration. Otherwise it restores the exact prior links. Until activation, the
// previous external CLI remains usable without requiring this recovery step.
func recoverMigration(root string) error {
	name := filepath.Join(root, ".pending-links.json")
	raw, _, exists, err := readRegular(name)
	if err != nil || !exists {
		return err
	}
	var j migrationJournal
	if err = json.Unmarshal(raw, &j); err != nil {
		return err
	}
	if filepath.Dir(j.Release) != filepath.Join(root, "releases") {
		return errors.New("invalid migration journal release")
	}
	current, err := filepath.EvalSymlinks(filepath.Join(root, "current"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	complete := current == j.Release
	for path, link := range j.Links {
		if !filepath.IsAbs(path) || link.Previous == "" || !managedTarget(root, link.Target) {
			return errors.New("invalid migration journal link")
		}
		actual, err := os.Readlink(path)
		if err != nil || actual != link.Previous && actual != link.Target {
			return fmt.Errorf("migration link changed outside installer: %s", path)
		}
	}
	for path, link := range j.Links {
		target := link.Previous
		if complete {
			target = link.Target
		}
		if err = replaceSymlink(path, target); err != nil {
			return err
		}
	}
	if complete {
		receipt := filepath.Join(root, ".managed-links.json")
		links := map[string]string{}
		if raw, e := os.ReadFile(receipt); e == nil {
			if e = json.Unmarshal(raw, &links); e != nil {
				return e
			}
		} else if !errors.Is(e, os.ErrNotExist) {
			return e
		}
		for path, link := range j.Links {
			links[path] = link.Target
		}
		raw, err := json.Marshal(links)
		if err != nil {
			return err
		}
		if err = atomicFile(receipt, raw, 0600); err != nil {
			return err
		}
	}
	return os.Remove(name)
}
func managedTarget(root, target string) bool {
	current := filepath.Join(root, "current")
	for _, relative := range []string{"bin/openarbi", "OpenArbi.app", "bundle/openarbi", "bundle/OpenArbi.app/Contents/MacOS/openarbi"} {
		if target == filepath.Join(current, relative) {
			return true
		}
	}
	return false
}
func compatibilityPaths(stage string) error {
	bundle := filepath.Join(stage, "bundle")
	if err := os.Mkdir(bundle, 0755); err != nil {
		return err
	}
	for name, target := range map[string]string{"openarbi": "../bin/openarbi", "OpenArbi.app": "../OpenArbi.app"} {
		if err := os.Symlink(target, filepath.Join(bundle, name)); err != nil {
			return err
		}
	}
	return nil
}
