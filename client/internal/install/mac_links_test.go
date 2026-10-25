package install

import (
	"archive/tar"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"
)

const testKittyContents = "lib/kitty/kitty.app/Contents/"

func macLinkArchive(t *testing.T, entries []*tar.Header) string {
	t.Helper()
	name := filepath.Join(t.TempDir(), "links.tar.gz")
	f, err := os.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	for _, h := range entries {
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return name
}

func macLinkFixture() []*tar.Header {
	framework := testKittyContents + "Frameworks/Python.framework/"
	quick := testKittyContents + "kitty-quick-access.app/Contents/"
	return []*tar.Header{
		{Name: framework + "Python", Typeflag: tar.TypeSymlink, Linkname: "Versions/Current/Python"},
		{Name: framework + "Resources", Typeflag: tar.TypeSymlink, Linkname: "Versions/Current/Resources"},
		{Name: framework + "Versions/Current", Typeflag: tar.TypeSymlink, Linkname: "3.12"},
		{Name: quick + "Frameworks", Typeflag: tar.TypeSymlink, Linkname: "../../Frameworks"},
		{Name: quick + "Resources", Typeflag: tar.TypeSymlink, Linkname: "../../Resources"},
		{Name: quick + "MacOS/kitten", Typeflag: tar.TypeSymlink, Linkname: "../../../MacOS/kitten"},
		{Name: quick + "MacOS/kitty", Typeflag: tar.TypeSymlink, Linkname: "../../../MacOS/kitty"},
		{Name: framework + "Versions/3.12/Python", Typeflag: tar.TypeReg, Mode: 0755},
		{Name: framework + "Versions/3.12/Resources/Info.plist", Typeflag: tar.TypeReg, Mode: 0644},
		{Name: testKittyContents + "Resources/theme", Typeflag: tar.TypeReg, Mode: 0644},
		{Name: testKittyContents + "MacOS/kitty", Typeflag: tar.TypeReg, Mode: 0755},
		{Name: testKittyContents + "MacOS/kitten", Typeflag: tar.TypeReg, Mode: 0755},
	}
}

func TestExtractPreservesPinnedMacLinks(t *testing.T) {
	entries := macLinkFixture()
	destination := t.TempDir()
	if err := Extract(t.Context(), macLinkArchive(t, entries), destination); err != nil {
		t.Fatal(err)
	}
	for _, h := range entries {
		if h.Typeflag != tar.TypeSymlink {
			continue
		}
		name := filepath.Join(destination, h.Name)
		if target, err := os.Readlink(name); err != nil || target != h.Linkname {
			t.Fatalf("link %s = %q, %v", h.Name, target, err)
		}
		if _, err := os.Stat(name); err != nil {
			t.Fatalf("unresolved link %s: %v", h.Name, err)
		}
	}
}

func TestExtractRejectsMacLinkAttacks(t *testing.T) {
	link := macLinkFixture()[0]
	tests := map[string][]*tar.Header{
		"unknown path":               {{Name: "unknown", Typeflag: tar.TypeSymlink, Linkname: "bin/kitty"}},
		"absolute target":            {{Name: link.Name, Typeflag: tar.TypeSymlink, Linkname: "/tmp"}},
		"escaped target":             {{Name: link.Name, Typeflag: tar.TypeSymlink, Linkname: "../../../../../../outside"}},
		"different in-bundle target": {{Name: link.Name, Typeflag: tar.TypeSymlink, Linkname: "Versions/Current/Other"}},
		"hardlink":                   {{Name: link.Name, Typeflag: tar.TypeLink, Linkname: link.Linkname}},
		"dangling target":            {link},
		"duplicate link":             {link, link},
		"link then child":            {link, {Name: link.Name + "/child", Typeflag: tar.TypeReg}},
		"child then link":            {{Name: link.Name + "/child", Typeflag: tar.TypeReg}, link},
		"file then link":             {{Name: link.Name, Typeflag: tar.TypeReg}, link},
		"link then directory":        {link, {Name: link.Name, Typeflag: tar.TypeDir}},
		"duplicate directory":        {{Name: "dir", Typeflag: tar.TypeDir}, {Name: "dir/", Typeflag: tar.TypeDir}},
		"non-directory ancestor":     {{Name: "lib", Typeflag: tar.TypeReg}, link},
	}
	for name, entries := range tests {
		t.Run(name, func(t *testing.T) {
			destination := t.TempDir()
			if err := Extract(t.Context(), macLinkArchive(t, entries), destination); err == nil {
				t.Fatal("unsafe archive accepted")
			}
		})
	}
}

func TestExtractRefusesPreexistingTargets(t *testing.T) {
	for _, symlinkRoot := range []bool{false, true} {
		t.Run(map[bool]string{false: "nested symlink", true: "destination symlink"}[symlinkRoot], func(t *testing.T) {
			destination, outside := t.TempDir(), t.TempDir()
			if symlinkRoot {
				destination = filepath.Join(destination, "stage")
				if err := os.Symlink(outside, destination); err != nil {
					t.Fatal(err)
				}
			} else if err := os.Symlink(outside, filepath.Join(destination, "lib")); err != nil {
				t.Fatal(err)
			}
			entries := []*tar.Header{{Name: "lib/written", Typeflag: tar.TypeReg, Mode: 0644}}
			if err := Extract(t.Context(), macLinkArchive(t, entries), destination); err == nil {
				t.Fatal("preexisting symlink accepted")
			}
			contents, err := os.ReadDir(outside)
			if err != nil {
				t.Fatal(err)
			}
			if len(contents) != 0 {
				t.Fatal("wrote through preexisting symlink")
			}
		})
	}
}

func TestExtractTruncatedMacArchiveCreatesNoLinks(t *testing.T) {
	name := macLinkArchive(t, macLinkFixture())
	data, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(name, data[:len(data)-8], 0600); err != nil {
		t.Fatal(err)
	}
	destination := t.TempDir()
	if err = Extract(t.Context(), name, destination); err == nil {
		t.Fatal("truncated archive accepted")
	}
	for _, h := range macLinkFixture() {
		if h.Typeflag == tar.TypeSymlink {
			if _, err := os.Lstat(filepath.Join(destination, h.Name)); !os.IsNotExist(err) {
				t.Fatalf("materialized link before CRC verification: %s (%v)", h.Name, err)
			}
		}
	}
}
