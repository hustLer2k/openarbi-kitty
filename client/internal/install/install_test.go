package install

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func payload(t *testing.T, extras ...*tar.Header) (string, string) {
	t.Helper()
	name := filepath.Join(t.TempDir(), "payload.tar.gz")
	f, err := os.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	for _, name := range []string{"bin/openarbi", "bin/openarbi-client", "bin/openarbi-desktop", "bin/openarbi-installer", "bin/kitty", "bin/codex", "bin/starship", "share/openarbi/theme/kitty.conf", "share/openarbi/desktop.py", "share/openarbi/assets/ready.wav"} {
		data := "fixture"
		if err = tw.WriteHeader(&tar.Header{Name: name, Mode: 0755, Size: int64(len(data)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err = tw.Write([]byte(data)); err != nil {
			t.Fatal(err)
		}
	}
	for _, h := range extras {
		if err = tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
	}
	if err = tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err = gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
	return name, checksum(t, name)
}
func checksum(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func options(t *testing.T, sum string) Options {
	t.Helper()
	base := filepath.Join(t.TempDir(), "home with spaces")
	return Options{Root: filepath.Join(base, "install"), Bin: filepath.Join(base, "bin/openarbi"), Version: "v1.0.0", SHA256: sum}
}
func TestInstallUpdateRollbackUninstall(t *testing.T) {
	archive, sum := payload(t)
	o := options(t, sum)
	first, err := InstallArchive(t.Context(), o, archive)
	if err != nil {
		t.Fatal(err)
	}
	target := func() string {
		t.Helper()
		s, err := filepath.EvalSymlinks(filepath.Join(o.Root, "current"))
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	if target() != first {
		t.Fatal("first release not active")
	}
	o.Version = "v1.0.1"
	o.Validate = func(context.Context, string) error { return errors.New("failed smoke test") }
	if _, err = InstallArchive(t.Context(), o, archive); err == nil {
		t.Fatal("failed validation accepted")
	}
	if target() != first {
		t.Fatal("failed update replaced old release")
	}
	o.Validate = nil
	o.SHA256 = strings.Repeat("0", 64)
	if _, err = InstallArchive(t.Context(), o, archive); err == nil {
		t.Fatal("corrupt checksum accepted")
	}
	if target() != first {
		t.Fatal("corrupt update replaced old release")
	}
	o.SHA256 = sum
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err = InstallArchive(ctx, o, archive); err == nil {
		t.Fatal("cancelled update accepted")
	}
	if target() != first {
		t.Fatal("cancelled update replaced old release")
	}
	second, err := InstallArchive(t.Context(), o, archive)
	if err != nil {
		t.Fatal(err)
	}
	if target() != second || first == second {
		t.Fatal("update did not switch atomically")
	}
	if _, err = os.Stat(first); err != nil {
		t.Fatal("prior release not retained")
	}
	preserved := filepath.Join(filepath.Dir(o.Root), "credentials")
	if err = os.WriteFile(preserved, []byte("preserve"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = Uninstall(o); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(preserved); err != nil {
		t.Fatal("uninstall removed unrelated data")
	}
	if _, err = os.Lstat(o.Bin); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("entrypoint left behind")
	}
}
func TestRejectArchiveAttacks(t *testing.T) {
	for _, h := range []*tar.Header{
		{Name: "../escaped", Typeflag: tar.TypeReg}, {Name: "/escaped", Typeflag: tar.TypeReg},
		{Name: "a/../../escaped", Typeflag: tar.TypeReg}, {Name: `a\escape`, Typeflag: tar.TypeReg},
		{Name: "link", Typeflag: tar.TypeSymlink, Linkname: "/tmp"}, {Name: "hard", Typeflag: tar.TypeLink, Linkname: "bin/openarbi"},
		{Name: "device", Typeflag: tar.TypeChar}, {Name: "bin/openarbi", Typeflag: tar.TypeReg},
	} {
		t.Run(h.Name, func(t *testing.T) {
			archive, sum := payload(t, h)
			o := options(t, sum)
			if _, err := InstallArchive(t.Context(), o, archive); err == nil {
				t.Fatal("unsafe archive accepted")
			}
			if _, err := os.Lstat(filepath.Join(o.Root, "current")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("unsafe release activated")
			}
		})
	}
}
func TestPreserveUnrelatedPath(t *testing.T) {
	for _, kind := range []string{"file", "symlink", "directory"} {
		t.Run(kind, func(t *testing.T) {
			archive, sum := payload(t)
			o := options(t, sum)
			if err := os.MkdirAll(filepath.Dir(o.Bin), 0755); err != nil {
				t.Fatal(err)
			}
			var err error
			switch kind {
			case "file":
				err = os.WriteFile(o.Bin, []byte("old CLI from unrelated installation"), 0755)
			case "directory":
				err = os.Mkdir(o.Bin, 0755)
			case "symlink":
				err = os.Symlink("/unrelated", o.Bin)
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err = InstallArchive(t.Context(), o, archive); err == nil {
				t.Fatal("unrelated entry overwritten")
			}
			if _, err = os.Lstat(o.Bin); err != nil {
				t.Fatal("unrelated entry removed")
			}
		})
	}
}
func TestTruncatedArchivePreservesPrevious(t *testing.T) {
	archive, sum := payload(t)
	o := options(t, sum)
	old, err := InstallArchive(t.Context(), o, archive)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(archive)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(archive, data[:len(data)-8], 0600); err != nil {
		t.Fatal(err)
	}
	o.SHA256 = checksum(t, archive)
	o.Version = "v1.0.1"
	if _, err = InstallArchive(t.Context(), o, archive); err == nil {
		t.Fatal("truncated archive accepted")
	}
	got, err := filepath.EvalSymlinks(filepath.Join(o.Root, "current"))
	if err != nil || got != old {
		t.Fatal("previous release not retained")
	}
}
func TestUnmanagedRootRefused(t *testing.T) {
	archive, sum := payload(t)
	o := options(t, sum)
	if err := os.MkdirAll(o.Root, 0755); err != nil {
		t.Fatal(err)
	}
	keep := filepath.Join(o.Root, "something-else")
	if err := os.WriteFile(keep, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := InstallArchive(t.Context(), o, archive); err == nil {
		t.Fatal("unmanaged root accepted")
	}
	if err := Uninstall(o); err == nil {
		t.Fatal("unmanaged root removed")
	}
}

func TestActivationFailurePreservesPrevious(t *testing.T) {
	archive, sum := payload(t)
	o := options(t, sum)
	old, err := InstallArchive(t.Context(), o, archive)
	if err != nil {
		t.Fatal(err)
	}
	o.Version = "v1.0.1"
	o.Validate = func(_ context.Context, stage string) error {
		return os.WriteFile(filepath.Join(o.Root, ".next-"+filepath.Base(stage)), []byte("activation blocked"), 0600)
	}
	if _, err = InstallArchive(t.Context(), o, archive); err == nil {
		t.Fatal("activation obstruction accepted")
	}
	current, err := filepath.EvalSymlinks(filepath.Join(o.Root, "current"))
	if err != nil || current != old {
		t.Fatal("activation failure lost previous release")
	}
}
func TestCancelledDownloadDoesNotCreateArchive(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	file := filepath.Join(t.TempDir(), "download")
	if err := Download(ctx, "https://example.invalid/payload", strings.Repeat("0", 64), file); err == nil {
		t.Fatal("cancelled download accepted")
	}
	if _, err := os.Stat(file); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("cancelled download left a payload")
	}
}

func TestStartupFailureRollsBackAndRestartsOld(t *testing.T) {
	archive, sum := payload(t)
	o := options(t, sum)
	old, err := InstallArchive(t.Context(), o, archive)
	if err != nil {
		t.Fatal(err)
	}
	var stopped, restored bool
	o.Version = "v1.0.1"
	o.BeforeActivate = func(_ context.Context, stage, previous string) (func() error, error) {
		if previous != old {
			t.Fatalf("wrong previous release %s", previous)
		}
		stopped = true
		return func() error {
			current, e := filepath.EvalSymlinks(filepath.Join(o.Root, "current"))
			if e != nil || current != old {
				t.Fatal("broker restarted before restoring previous payload")
			}
			restored = true
			return nil
		}, nil
	}
	o.AfterActivate = func(_ context.Context, stage string) error {
		current, e := filepath.EvalSymlinks(filepath.Join(o.Root, "current"))
		if e != nil || current != stage {
			t.Fatal("new broker started before payload switch")
		}
		return errors.New("new broker failed")
	}
	if _, err = InstallArchive(t.Context(), o, archive); err == nil {
		t.Fatal("startup failure accepted")
	}
	if !stopped || !restored {
		t.Fatal("lifecycle rollback did not run")
	}
}
