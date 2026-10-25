// Package install owns verified release staging and atomic activation. It never
// reads credentials or touches remote rooms.
package install

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"
)

const marker = "openarbi-managed-install-v1\n"

var versionPattern = regexp.MustCompile(`^v?[0-9]+\.[0-9]+\.[0-9]+(?:-[A-Za-z0-9.-]+)?$`)

type Options struct {
	Root, Bin, App, Version, URL, SHA256 string
	LegacyRoot                           string
	Progress                             func(phase, detail string)
	// Validate runs before activation; errors leave the previous release active.
	Validate func(context.Context, string) error
	// BeforeActivate may quiesce an existing broker after full payload validation.
	// Its undo is called only on failure, after restoring the previous symlink.
	BeforeActivate func(context.Context, string, string) (func() error, error)
	AfterActivate  func(context.Context, string) error
}

func (o Options) report(phase, detail string) {
	if o.Progress != nil {
		o.Progress(phase, detail)
	}
}

func Download(ctx context.Context, rawURL, digest, destination string) error {
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return errors.New("release URL must use HTTPS")
	}
	sum, err := hex.DecodeString(digest)
	if err != nil || len(sum) != sha256.Size {
		return errors.New("release requires a SHA-256 checksum")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 15 * time.Minute, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if req.URL.Scheme != "https" {
			return errors.New("refusing non-HTTPS redirect")
		}
		if len(via) >= 10 {
			return errors.New("too many redirects")
		}
		return nil
	}}
	response, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("download release: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("download release: HTTP %d", response.StatusCode)
	}
	f, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	h := sha256.New()
	_, err = io.Copy(io.MultiWriter(f, h), io.LimitReader(response.Body, 2<<30))
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if hex.EncodeToString(h.Sum(nil)) != strings.ToLower(digest) {
		return errors.New("release checksum mismatch; previous installation preserved")
	}
	return nil
}

// Extract accepts directories, ordinary files and the exact pinned macOS
// framework links. Links are created only after all file writes and CRC checks.
// Staging must begin empty so no archive write can follow a preexisting link.
func Extract(ctx context.Context, archive, destination string) error {
	info, err := os.Lstat(destination)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return errors.New("extraction destination must be a real directory")
	}
	entries, err := os.ReadDir(destination)
	if err != nil {
		return err
	}
	if len(entries) != 0 {
		return errors.New("extraction destination must be empty")
	}
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()
	reader := tar.NewReader(gz)
	var total int64
	seen := map[string]byte{}
	parents := map[string]bool{}
	links := map[string]string{}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		h, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("read payload: %w", err)
		}
		name := strings.TrimSuffix(h.Name, "/")
		if name == "" || name == "." {
			if h.Typeflag == tar.TypeDir {
				continue
			}
			return errors.New("invalid archive entry")
		}
		if strings.Contains(name, `\`) || !filepath.IsLocal(name) || path.Clean(name) != name {
			return fmt.Errorf("unsafe archive path %q", name)
		}
		if _, exists := seen[name]; exists {
			return fmt.Errorf("duplicate archive entry %q", name)
		}
		for parent := path.Dir(name); parent != "."; parent = path.Dir(parent) {
			if kind, exists := seen[parent]; exists && kind != tar.TypeDir {
				return fmt.Errorf("archive entry %q has a non-directory ancestor", name)
			}
			parents[parent] = true
		}
		if h.Typeflag != tar.TypeDir && parents[name] {
			return fmt.Errorf("archive entry %q conflicts with earlier children", name)
		}
		seen[name] = h.Typeflag
		target := filepath.Join(destination, filepath.FromSlash(name))
		switch h.Typeflag {
		case tar.TypeDir:
			if err = os.MkdirAll(target, 0755); err != nil {
				return err
			}
		case tar.TypeSymlink:
			expected, allowed := macBundleLinks[name]
			if !allowed || h.Linkname != expected {
				return fmt.Errorf("unapproved archive link %q", name)
			}
			links[name] = h.Linkname
		case tar.TypeReg:
			total += h.Size
			if h.Size < 0 || total > 4<<30 {
				return errors.New("payload exceeds extraction limit")
			}
			if err = os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				return err
			}
			mode := os.FileMode(0644)
			if h.Mode&0111 != 0 {
				mode = 0755
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(out, reader)
			closeErr := out.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
		default:
			return fmt.Errorf("unsupported archive entry %q (unapproved links and special files are forbidden)", name)
		}
	}
	// tar may stop before the gzip trailer; drain to verify its CRC and detect truncation.
	if _, err = io.Copy(io.Discard, gz); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	return materializeMacLinks(destination, links)
}

func lock(root string) (*os.File, error) {
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(root, ".lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, errors.New("another OpenArbi installer is running")
	}
	return f, nil
}
func checkLink(name, target string) error {
	if name == "" {
		return nil
	}
	info, err := os.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink == 0 {
		return fmt.Errorf("refusing to overwrite unrelated path %s; move it explicitly first", name)
	}
	got, err := os.Readlink(name)
	if err != nil {
		return err
	}
	if got != target {
		return fmt.Errorf("refusing to overwrite unrelated symlink %s", name)
	}
	return nil
}
func validateRoot(root string) error {
	contents, err := os.ReadFile(filepath.Join(root, ".managed"))
	if err == nil && string(contents) == marker {
		return nil
	}
	legacy, legacyErr := inspectLegacy(root)
	if legacyErr != nil {
		return legacyErr
	}
	if legacy != nil {
		return os.WriteFile(filepath.Join(root, ".managed"), []byte(marker), 0600)
	}
	entries, readErr := os.ReadDir(root)
	if readErr != nil {
		return readErr
	}
	for _, e := range entries {
		if e.Name() != ".lock" {
			return errors.New("installation directory is not managed by OpenArbi")
		}
	}
	return os.WriteFile(filepath.Join(root, ".managed"), []byte(marker), 0600)
}
func ValidatePayload(_ context.Context, stage string) error {
	for _, name := range []string{"bin/openarbi", "bin/openarbi-client", "bin/openarbi-desktop", "bin/openarbi-installer", "bin/kitty", "bin/codex", "bin/starship"} {
		info, err := os.Stat(filepath.Join(stage, name))
		if err != nil {
			return fmt.Errorf("incomplete payload: %s: %w", name, err)
		}
		if !info.Mode().IsRegular() || info.Mode()&0111 == 0 {
			return fmt.Errorf("payload executable missing: %s", name)
		}
	}
	for _, name := range []string{"share/openarbi/theme/kitty.conf", "share/openarbi/desktop.py", "share/openarbi/assets/ready.wav"} {
		if _, err := os.Stat(filepath.Join(stage, name)); err != nil {
			return fmt.Errorf("incomplete payload: %s", name)
		}
	}
	return nil
}

// InstallArchive activates an already downloaded archive only after verifying its
// checksum again. All owned files live under Root; public entrypoints are exact links.
func InstallArchive(ctx context.Context, o Options, archive string) (result string, resultErr error) {
	if !versionPattern.MatchString(o.Version) {
		return "", errors.New("an explicit semantic release version is required")
	}
	if !filepath.IsAbs(o.Root) || !filepath.IsAbs(o.Bin) {
		return "", errors.New("installation paths must be absolute")
	}
	o.report("verifying", "Checking release checksum")
	f, err := os.Open(archive)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	_, err = io.Copy(h, f)
	f.Close()
	if err != nil {
		return "", err
	}
	if len(o.SHA256) != 64 || hex.EncodeToString(h.Sum(nil)) != strings.ToLower(o.SHA256) {
		return "", errors.New("release checksum mismatch; previous installation preserved")
	}
	guard, err := lock(o.Root)
	if err != nil {
		return "", err
	}
	defer guard.Close()
	if err = recoverMigration(o.Root); err != nil {
		return "", err
	}
	if err = validateRoot(o.Root); err != nil {
		return "", err
	}
	legacy, err := inspectLegacy(o.Root)
	if err != nil {
		return "", err
	}
	current := filepath.Join(o.Root, "current")
	previous := ""
	if info, e := os.Lstat(current); e == nil {
		if info.Mode()&os.ModeSymlink == 0 {
			return "", errors.New("current release is not a managed symlink")
		}
		target, e := os.Readlink(current)
		previous = target
		if e != nil || (legacy == nil && (filepath.Dir(target) != "releases" || !filepath.IsLocal(target))) {
			return "", errors.New("current release points outside managed releases")
		}
	} else if !errors.Is(e, os.ErrNotExist) {
		return "", e
	}
	links := map[string]string{o.Bin: filepath.Join(current, "bin/openarbi")}
	if o.App != "" {
		links[o.App] = filepath.Join(current, "OpenArbi.app")
	}
	migrations := map[string]string{}
	legacyCLI := filepath.Join(o.Root, "bin/openarbi")
	if legacy != nil {
		// Compatibility paths preserve the old CLI until the single current switch.
		links[legacyCLI] = legacy.cliTarget
	}
	if o.LegacyRoot != "" && o.LegacyRoot != o.Root {
		oldCLI := filepath.Join(o.LegacyRoot, "bin/openarbi")
		if target, e := os.Readlink(oldCLI); e == nil && target == filepath.Join(current, "bin/openarbi") {
			links[oldCLI] = target
		} else {
			external, e := inspectLegacy(o.LegacyRoot)
			if e != nil {
				return "", e
			}
			if external != nil {
				links[oldCLI] = filepath.Join(current, "bin/openarbi")
				migrations[oldCLI] = external.cliTarget
				if o.App != "" {
					expected := filepath.Join(o.LegacyRoot, "current/bundle/OpenArbi.app")
					if actual, e := os.Readlink(o.App); e == nil && actual == expected {
						migrations[o.App] = expected
					}
				}
			}
		}
	}
	if raw, e := os.ReadFile(filepath.Join(o.Root, ".managed-links.json")); e == nil {
		var stored map[string]string
		if e = json.Unmarshal(raw, &stored); e != nil {
			return "", e
		}
		for name, target := range stored {
			if !filepath.IsAbs(name) || !managedTarget(o.Root, target) {
				return "", errors.New("invalid managed links receipt")
			}
			links[name] = target
		}
	} else if !errors.Is(e, os.ErrNotExist) {
		return "", e
	}
	for name, target := range links {
		if before, ok := migrations[name]; ok {
			if err = checkLink(name, before); err != nil {
				return "", err
			}
			continue
		}
		if err = checkLink(name, target); err != nil {
			return "", err
		}
	}
	releases := filepath.Join(o.Root, "releases")
	if err = os.MkdirAll(releases, 0700); err != nil {
		return "", err
	}
	stage, err := os.MkdirTemp(releases, o.Version+"-")
	if err != nil {
		return "", err
	}
	active := false
	rollbackFailed := false
	rollbackError := func(e error) {
		if e != nil {
			rollbackFailed = true
			resultErr = errors.Join(resultErr, e)
		}
	}
	defer func() {
		if !active && !rollbackFailed {
			_ = os.RemoveAll(stage)
		}
	}()
	defer func() {
		if active || !rollbackFailed {
			_ = os.Remove(filepath.Join(o.Root, ".pending-links.json"))
		}
	}()
	o.report("installing", "Preparing new release")
	if err = Extract(ctx, archive, stage); err != nil {
		return "", err
	}
	if err = compatibilityPaths(stage); err != nil {
		return "", err
	}
	if err = ValidatePayload(ctx, stage); err != nil {
		return "", err
	}
	if o.Validate != nil {
		if err = o.Validate(ctx, stage); err != nil {
			return "", fmt.Errorf("release validation: %w", err)
		}
	}
	if err = ctx.Err(); err != nil {
		return "", err
	}
	// Entry points are checked before touching the active release. Roll back only
	// newly created links when activation fails; existing links remain untouched.
	var created []string
	defer func() {
		if !active {
			for _, name := range created {
				_ = os.Remove(name)
			}
		}
	}()
	changedLegacy := map[string]string{}
	defer func() {
		if !active {
			for name, target := range changedLegacy {
				temporary := name + ".openarbi-restore-" + filepath.Base(stage)
				if e := os.Symlink(target, temporary); e != nil {
					rollbackError(e)
				} else {
					rollbackError(os.Rename(temporary, name))
					_ = os.Remove(temporary)
				}
			}
		}
	}()
	for name, target := range links {
		if _, ok := migrations[name]; ok {
			continue
		} // External CLI stays on the old installation until activation.
		if _, e := os.Lstat(name); errors.Is(e, os.ErrNotExist) {
			if err = os.MkdirAll(filepath.Dir(name), 0755); err != nil {
				return "", err
			}
			if err = os.Symlink(target, name); err != nil {
				return "", err
			}
			created = append(created, name)
		}
	}
	oldReceipt, receiptMode, receiptExisted, err := readRegular(filepath.Join(o.Root, ".managed-links.json"))
	if err != nil {
		return "", err
	}
	receiptChanged := false
	defer func() {
		if !active && receiptChanged {
			var e error
			if receiptExisted {
				e = atomicFile(filepath.Join(o.Root, ".managed-links.json"), oldReceipt, receiptMode)
			} else {
				e = os.Remove(filepath.Join(o.Root, ".managed-links.json"))
			}
			rollbackError(e)
		}
	}()
	if err = writeMigrationJournal(o.Root, stage, migrations, links); err != nil {
		return "", err
	}
	next := filepath.Join(o.Root, ".next-"+filepath.Base(stage))
	if err = os.Symlink("releases/"+filepath.Base(stage), next); err != nil {
		return "", err
	}
	defer os.Remove(next)
	var undo func() error
	if o.BeforeActivate != nil {
		old := ""
		if previous != "" {
			old = previous
			if !filepath.IsAbs(old) {
				old = filepath.Join(o.Root, old)
			}
		}
		if legacy != nil {
			old = ""
		}
		undo, err = o.BeforeActivate(ctx, stage, old)
		if err != nil {
			return "", err
		}
	}
	switched := false
	defer func() {
		if !active {
			if switched {
				if previous == "" {
					rollbackError(os.Remove(current))
				} else {
					rollback := filepath.Join(o.Root, ".rollback-"+filepath.Base(stage))
					if e := os.Symlink(previous, rollback); e != nil {
						rollbackError(e)
					} else {
						rollbackError(os.Rename(rollback, current))
						_ = os.Remove(rollback)
					}
				}
			}
			if undo != nil {
				rollbackError(undo())
			}
		}
	}()
	if err = ctx.Err(); err != nil {
		return "", err
	}
	if err = os.Rename(next, current); err != nil {
		return "", fmt.Errorf("activate release: %w", err)
	}
	switched = true
	// External entrypoints change only after the canonical payload is active.
	for name, previous := range migrations {
		if err = replaceSymlink(name, links[name]); err != nil {
			return "", err
		}
		changedLegacy[name] = previous
	}
	receipt, err := json.Marshal(links)
	if err != nil {
		return "", err
	}
	if err = atomicFile(filepath.Join(o.Root, ".managed-links.json"), append(receipt, '\n'), 0600); err != nil {
		return "", err
	}
	receiptChanged = true
	// No fallible filesystem finalization follows successful broker startup.
	if o.AfterActivate != nil {
		if err = o.AfterActivate(ctx, stage); err != nil {
			return "", fmt.Errorf("new release startup failed; restored previous installation: %w", err)
		}
	}
	active = true
	return stage, nil
}

func Uninstall(o Options) error {
	guard, err := lock(o.Root)
	if err != nil {
		return err
	}
	defer guard.Close()
	if err = recoverMigration(o.Root); err != nil {
		return err
	}
	raw, err := os.ReadFile(filepath.Join(o.Root, ".managed"))
	if err != nil || string(raw) != marker {
		return errors.New("refusing to remove an unmanaged installation")
	}
	links := map[string]string{o.Bin: filepath.Join(o.Root, "current/bin/openarbi")}
	if o.App != "" {
		links[o.App] = filepath.Join(o.Root, "current/OpenArbi.app")
	}
	if raw, e := os.ReadFile(filepath.Join(o.Root, ".managed-links.json")); e == nil {
		var stored map[string]string
		if e = json.Unmarshal(raw, &stored); e != nil {
			return e
		}
		for name, target := range stored {
			if !filepath.IsAbs(name) || !managedTarget(o.Root, target) {
				return errors.New("invalid managed links receipt")
			}
			links[name] = target
		}
	} else if !errors.Is(e, os.ErrNotExist) {
		return e
	}
	for name, target := range links {
		if err = checkLink(name, target); err != nil {
			return err
		}
	}
	for name := range links {
		if err = os.Remove(name); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	// Deliberately retain root/lock: unlinking a held lock permits concurrent writers.
	for _, name := range []string{"current", "releases"} {
		if err = os.RemoveAll(filepath.Join(o.Root, name)); err != nil {
			return err
		}
	}
	return nil
}

// CurrentRelease proves the executable path belongs to this managed installation
// before an updater/remover invokes lifecycle commands from it.
func CurrentRelease(root string) (string, error) {
	raw, _, exists, err := readRegular(filepath.Join(root, ".managed"))
	if err != nil {
		return "", err
	}
	if !exists || string(raw) != marker {
		return "", errors.New("installation is not managed by OpenArbi")
	}
	// Darwin's /var and /tmp (and user-selected parent directories) may be
	// aliases. Compare both sides in the same namespace, but do not resolve the
	// releases directory separately: redirecting it outside the root is invalid.
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	current, err := filepath.EvalSymlinks(filepath.Join(root, "current"))
	if err != nil {
		return "", err
	}
	if filepath.Dir(current) != filepath.Join(root, "releases") {
		return "", errors.New("active release escapes managed directory")
	}
	return current, nil
}
