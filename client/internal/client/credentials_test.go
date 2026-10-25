package client

import (
	"errors"
	"testing"

	"github.com/zalando/go-keyring"
)

// These tests replace the OS backend with an isolated in-memory provider before
// any access. They never read or change the operator's actual Keychain entries.
func TestCredentialRemovalOnlyDeletesSelectedOrigin(t *testing.T) {
	keyring.MockInit()
	vault := Credentials{}
	const selected = "https://selected.example.invalid"
	const unrelated = "https://unrelated.example.invalid"
	if err := vault.Set(selected, "synthetic-selected"); err != nil {
		t.Fatal(err)
	}
	if err := vault.Set(unrelated, "synthetic-unrelated"); err != nil {
		t.Fatal(err)
	}
	if err := vault.Delete(selected); err != nil {
		t.Fatal(err)
	}
	if _, err := vault.Get(selected); err == nil {
		t.Fatal("deleted credential remains available")
	}
	if got, err := vault.Get(unrelated); err != nil || got != "synthetic-unrelated" {
		t.Fatal("unrelated credential changed")
	}
}

func TestCredentialRemovalReportsStoreFailure(t *testing.T) {
	keyring.MockInitWithError(errors.New("synthetic locked store"))
	t.Cleanup(keyring.MockInit)
	if err := (Credentials{}).Delete("https://selected.example.invalid"); err == nil {
		t.Fatal("credential removal failure was hidden")
	}
}
