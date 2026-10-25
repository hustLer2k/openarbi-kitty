package client

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const initialServer = "http://127.0.0.1:8090"

// Preferences contains only the last successfully authenticated API origin.
// Credentials remain exclusively in the OS credential store.
type Preferences struct{ Path string }

func UserPreferences() (Preferences, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return Preferences{}, errors.New("could not locate the user configuration directory")
	}
	return Preferences{filepath.Join(base, "OpenArbi", "client.json")}, nil
}
func (p Preferences) Server() (string, error) {
	raw, err := os.ReadFile(p.Path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", errors.New("could not read saved API origin; use --server explicitly")
	}
	var settings struct {
		Server string `json:"server"`
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&settings) != nil || settings.Server == "" {
		return "", errors.New("saved API origin is invalid; use --server and log in to repair it")
	}
	if decoder.Decode(new(any)) != io.EOF {
		return "", errors.New("saved API origin is invalid; use --server and log in to repair it")
	}
	server, err := NormalizeOrigin(settings.Server)
	if err != nil {
		return "", errors.New("saved API origin is invalid; use --server and log in to repair it")
	}
	return server, nil
}
func (p Preferences) SaveServer(server string) error {
	normalized, err := NormalizeOrigin(server)
	if err != nil {
		return err
	}
	if p.Path == "" {
		return errors.New("could not save API origin: configuration path is unavailable")
	}
	directory := filepath.Dir(p.Path)
	if err = os.MkdirAll(directory, 0700); err != nil {
		return errors.New("could not create the API preference directory")
	}
	file, err := os.CreateTemp(directory, ".client-*.json")
	if err != nil {
		return errors.New("could not create the API preference file")
	}
	name := file.Name()
	defer func() { _ = os.Remove(name) }()
	if err = file.Chmod(0600); err == nil {
		err = json.NewEncoder(file).Encode(struct {
			Server string `json:"server"`
		}{normalized})
	}
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(name, p.Path)
	}
	if err != nil {
		return errors.New("could not atomically save the API origin")
	}
	return nil
}

// ResolveServer preserves explicit flag > environment > saved login priority.
// Invalid saved state is an error rather than a switch to a different server.
func ResolveServer(explicit, environment string, p Preferences) (string, error) {
	if explicit != "" {
		return NormalizeOrigin(explicit)
	}
	if environment != "" {
		return NormalizeOrigin(environment)
	}
	stored, err := p.Server()
	if err != nil {
		return "", err
	}
	if stored != "" {
		return stored, nil
	}
	return initialServer, nil
}
