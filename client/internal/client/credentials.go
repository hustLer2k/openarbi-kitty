package client

import (
	"errors"
	"strings"

	"github.com/zalando/go-keyring"
)

const credentialService = "OpenArbi"

// Credentials uses the platform credential store: Keychain on macOS, Secret
// Service on Linux, Credential Manager on Windows. Tokens never enter files.
type Credentials struct{}

func (Credentials) Get(server string) (string, error) {
	token, err := keyring.Get(credentialService, server)
	if err != nil {
		return "", errors.New("no saved token; run openarbi login")
	}
	return token, nil
}
func (Credentials) Set(server, token string) error {
	if strings.TrimSpace(token) == "" {
		return errors.New("empty token")
	}
	if err := keyring.Set(credentialService, server, token); err != nil {
		return errors.New("could not save token in OS credential store")
	}
	return nil
}
func (Credentials) Delete(server string) error {
	if err := keyring.Delete(credentialService, server); err != nil {
		return errors.New("could not delete saved token")
	}
	return nil
}
