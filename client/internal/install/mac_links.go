package install

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

const kittyApp = "lib/kitty/kitty.app"
const pythonFramework = kittyApp + "/Contents/Frameworks/Python.framework/"
const quickAccess = kittyApp + "/Contents/kitty-quick-access.app/Contents/"

// These are the exact relative links in the locked Kitty 0.43.1 macOS bundle.
// Framework links must survive packaging for macOS code signatures to validate.
// Updating Kitty requires explicitly reviewing this inventory; arbitrary archive
// links remain forbidden on every platform.
var macBundleLinks = map[string]string{
	pythonFramework + "Python":           "Versions/Current/Python",
	pythonFramework + "Resources":        "Versions/Current/Resources",
	pythonFramework + "Versions/Current": "3.12",
	quickAccess + "Frameworks":           "../../Frameworks",
	quickAccess + "Resources":            "../../Resources",
	quickAccess + "MacOS/kitten":         "../../../MacOS/kitten",
	quickAccess + "MacOS/kitty":          "../../../MacOS/kitty",
}

func materializeMacLinks(destination string, links map[string]string) (err error) {
	if len(links) == 0 {
		return nil
	}
	app, err := filepath.EvalSymlinks(filepath.Join(destination, kittyApp))
	if err != nil {
		return fmt.Errorf("locate Kitty app for framework links: %w", err)
	}
	var names []string
	for name := range links {
		names = append(names, name)
	}
	slices.Sort(names)
	var created []string
	defer func() {
		if err != nil {
			for _, name := range created {
				err = errors.Join(err, os.Remove(name))
			}
		}
	}()
	// No ordinary extraction writes happen after this point. Creating all links
	// first allows Python -> Versions/Current/Python regardless of archive order.
	for _, name := range names {
		target := filepath.Join(destination, filepath.FromSlash(name))
		if err = os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return err
		}
		if err = os.Symlink(links[name], target); err != nil {
			return err
		}
		created = append(created, target)
	}
	for _, name := range created {
		resolved, e := filepath.EvalSymlinks(name)
		if e != nil {
			return fmt.Errorf("resolve bundled framework link: %w", e)
		}
		relative, e := filepath.Rel(app, resolved)
		if e != nil || relative == "." || !filepath.IsLocal(relative) || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return errors.New("bundled framework link escapes Kitty app")
		}
	}
	return nil
}
