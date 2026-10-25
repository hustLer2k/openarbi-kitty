package desktop

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

const maxStateBytes = 4 << 20

type FileStore struct{ Directory string }

func UserDirectory() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", errors.New("cannot locate user configuration directory")
	}
	return filepath.Join(base, "OpenArbi", "desktop"), nil
}

// PrivateDirectory rejects a preexisting public directory or symlink instead of
// silently repairing an attacker-controlled socket/state location.
func PrivateDirectory(path string) error {
	if !filepath.IsAbs(path) {
		return errors.New("desktop directory must be absolute")
	}
	if err := os.MkdirAll(path, 0700); err != nil {
		return errors.New("cannot create desktop directory")
	}
	st, err := os.Lstat(path)
	if err != nil {
		return err
	}
	owner, ok := st.Sys().(*syscall.Stat_t)
	if !st.IsDir() || st.Mode()&os.ModeSymlink != 0 || st.Mode().Perm()&0077 != 0 || !ok || int(owner.Uid) != os.Getuid() {
		return errors.New("desktop directory must be private (0700) and owned by the current user")
	}
	return nil
}
func (f FileStore) Load() (Workspace, error) {
	var s Workspace
	if err := PrivateDirectory(f.Directory); err != nil {
		return s, err
	}
	path := filepath.Join(f.Directory, "workspace.json")
	st, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return s, errors.New("cannot inspect saved workspace")
	}
	if !st.Mode().IsRegular() || st.Mode().Perm()&0077 != 0 || st.Size() > maxStateBytes {
		return s, errors.New("saved workspace must be a private regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return s, errors.New("cannot read saved workspace")
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, maxStateBytes+1))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&s); err != nil {
		return Workspace{}, errors.New("saved workspace is invalid; preserve it for recovery")
	}
	if decoder.Decode(new(any)) != io.EOF {
		return Workspace{}, errors.New("saved workspace has trailing or oversized data")
	}
	if err = validate(s); err != nil {
		return Workspace{}, err
	}
	return s, nil
}
func (f FileStore) Save(s Workspace) error {
	if err := validate(s); err != nil {
		return err
	}
	if err := PrivateDirectory(f.Directory); err != nil {
		return err
	}
	raw, err := json.Marshal(s)
	if err != nil || len(raw) > maxStateBytes {
		return errors.New("workspace snapshot exceeds the supported size")
	}
	file, err := os.CreateTemp(f.Directory, ".workspace-*")
	if err != nil {
		return errors.New("cannot create workspace snapshot")
	}
	name := file.Name()
	defer os.Remove(name)
	if err = file.Chmod(0600); err == nil {
		_, err = file.Write(raw)
	}
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(name, filepath.Join(f.Directory, "workspace.json"))
	}
	if err != nil {
		return errors.New("cannot durably save workspace")
	}
	dir, err := os.Open(f.Directory)
	if err != nil {
		return errors.New("cannot sync workspace directory")
	}
	defer dir.Close()
	if err = dir.Sync(); err != nil {
		return errors.New("cannot sync workspace directory; restart before further changes")
	}
	return nil
}
