package install

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

type legacyEntry struct {
	SHA256    string `json:"sha256,omitempty"`
	Link      string `json:"link,omitempty"`
	Directory bool   `json:"directory,omitempty"`
	Mode      uint32 `json:"mode,omitempty"`
}
type legacyReceipt struct {
	Schema       int                    `json:"schema"`
	Platform     string                 `json:"platform"`
	CodexVersion string                 `json:"codex_version"`
	Source       string                 `json:"source_sha256"`
	Entries      map[string]legacyEntry `json:"entries"`
	Checksums    map[string]string      `json:"checksums"`
}
type legacyInstall struct{ release, cliTarget string }

func fileDigest(name string) (string, error) {
	f, err := os.Open(name)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err = io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
func ownedRegular(name string) error {
	info, err := os.Lstat(name)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("legacy receipt/file is not regular: %s", name)
	}
	return nil
}

// inspectLegacy accepts only the installed receipt formats emitted by the prior
// CLI installer. Names alone never establish ownership; every recorded digest,
// mode and link is verified before a managed entrypoint can be replaced.
func inspectLegacy(root string) (*legacyInstall, error) {
	current := filepath.Join(root, "current")
	target, err := os.Readlink(current)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	release := target
	if !filepath.IsAbs(release) {
		release = filepath.Join(root, release)
	}
	release = filepath.Clean(release)
	if filepath.Dir(release) != filepath.Join(root, "releases") {
		return nil, errors.New("legacy current link escapes its releases directory")
	}
	receipt := filepath.Join(release, "installation.json")
	if _, err = os.Lstat(receipt); errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err = ownedRegular(receipt); err != nil {
		return nil, err
	}
	info, err := os.Lstat(release)
	if err != nil || !info.IsDir() {
		return nil, errors.New("legacy release directory is not regular")
	}
	resolvedRelease, err := filepath.EvalSymlinks(release)
	if err != nil {
		return nil, fmt.Errorf("resolve legacy release directory: %w", err)
	}
	data, err := os.ReadFile(receipt)
	if err != nil {
		return nil, err
	}
	var r legacyReceipt
	if err = json.Unmarshal(data, &r); err != nil {
		return nil, err
	}
	if r.Source == "" || r.CodexVersion != "0.164.0" {
		return nil, errors.New("legacy receipt lacks ownership provenance")
	}
	cli := "bundle/openarbi"
	if r.Schema != 2 && strings.HasPrefix(r.Platform, "Darwin/") {
		cli = "bundle/OpenArbi.app/Contents/MacOS/openarbi"
	}
	codex := filepath.ToSlash(filepath.Join(filepath.Dir(cli), "codex"))
	if r.Schema == 2 {
		if _, ok := r.Entries[cli]; !ok {
			return nil, errors.New("legacy receipt lacks CLI")
		}
		if _, ok := r.Entries[codex]; !ok {
			return nil, errors.New("legacy receipt lacks Codex")
		}
		seen := map[string]bool{}
		err = filepath.WalkDir(filepath.Join(release, "bundle"), func(name string, d os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			rel, _ := filepath.Rel(release, name)
			rel = filepath.ToSlash(rel)
			if rel == "bundle" {
				if d.Type()&os.ModeSymlink != 0 {
					return errors.New("legacy bundle is a symlink")
				}
				return nil
			}
			expected, ok := r.Entries[rel]
			if !ok {
				return fmt.Errorf("legacy receipt omits %s", rel)
			}
			seen[rel] = true
			info, err := os.Lstat(name)
			if err != nil {
				return err
			}
			switch {
			case expected.Directory:
				if !info.IsDir() {
					return errors.New("legacy directory changed")
				}
			case expected.Link != "":
				got, err := os.Readlink(name)
				if err != nil || got != expected.Link {
					return errors.New("legacy link changed")
				}
				resolved, err := filepath.EvalSymlinks(name)
				if err != nil || !strings.HasPrefix(resolved, resolvedRelease+string(filepath.Separator)) {
					return errors.New("legacy link escapes release")
				}
			case expected.SHA256 != "":
				if !info.Mode().IsRegular() || uint32(info.Mode().Perm()) != expected.Mode {
					return errors.New("legacy file type or mode changed")
				}
				got, err := fileDigest(name)
				if err != nil || got != expected.SHA256 {
					return errors.New("legacy checksum mismatch")
				}
			default:
				return errors.New("invalid legacy entry")
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
		if len(seen) != len(r.Entries) {
			return nil, errors.New("legacy receipt contains missing entries")
		}
	} else {
		if r.Checksums[cli] == "" || r.Checksums[codex] == "" {
			return nil, errors.New("legacy receipt lacks executable checksums")
		}
		for relative, digest := range r.Checksums {
			if !filepath.IsLocal(relative) {
				return nil, errors.New("unsafe legacy checksum path")
			}
			name := filepath.Join(release, relative)
			resolved, err := filepath.EvalSymlinks(name)
			if err != nil || !strings.HasPrefix(resolved, resolvedRelease+string(filepath.Separator)) {
				return nil, errors.New("legacy checksum path escapes release")
			}
			got, err := fileDigest(name)
			if err != nil || got != digest {
				return nil, errors.New("legacy checksum mismatch")
			}
		}
	}
	oldCLI := filepath.Join(root, "bin/openarbi")
	expected := filepath.Join(root, "current", filepath.FromSlash(cli))
	if err = checkLink(oldCLI, expected); err != nil {
		return nil, err
	}
	return &legacyInstall{release, expected}, nil
}
