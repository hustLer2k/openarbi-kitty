package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hustLer2k/openarbi-kitty/client/internal/client"
)

type fakeVault struct {
	stored bool
	err    error
}

func (v *fakeVault) Set(string, string) error { v.stored = true; return v.err }
func TestOnlySuccessfulLoginPersistsOrigin(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusOK} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(status)
				if status == 200 {
					fmt.Fprint(w, `{"version":"1"}`)
				} else {
					fmt.Fprint(w, `{"code":"unauthorized","message":"Login rejected"}`)
				}
			}))
			defer server.Close()
			api, err := client.New(server.URL, "fixture-private-token", server.Client())
			if err != nil {
				t.Fatal(err)
			}
			p := client.Preferences{Path: filepath.Join(t.TempDir(), "client.json")}
			if err = p.SaveServer("https://previous.invalid"); err != nil {
				t.Fatal(err)
			}
			vault := &fakeVault{}
			err = saveLogin(context.Background(), api, server.URL, "fixture-private-token", vault, p)
			stored, _ := p.Server()
			if status == 200 {
				if err != nil || stored != server.URL || !vault.stored {
					t.Fatal(stored, err)
				}
			} else {
				if err == nil || stored != "https://previous.invalid" || vault.stored {
					t.Fatal("failed login switched server")
				}
			}
			raw, _ := os.ReadFile(p.Path)
			if strings.Contains(string(raw), "fixture-private-token") {
				t.Fatal("token persisted outside credential store")
			}
		})
	}
}
func TestCredentialStoreFailureDoesNotSwitchOrigin(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"version":"1"}`) }))
	defer server.Close()
	api, _ := client.New(server.URL, "fixture", server.Client())
	p := client.Preferences{Path: filepath.Join(t.TempDir(), "client.json")}
	_ = p.SaveServer("https://previous.invalid")
	err := saveLogin(t.Context(), api, server.URL, "fixture", &fakeVault{err: errors.New("store unavailable")}, p)
	stored, _ := p.Server()
	if err == nil || stored != "https://previous.invalid" {
		t.Fatal("credential failure switched origin")
	}
}

func TestLegacyCommandIsRejectedBeforeCredentialLookup(t *testing.T) {
	previousArgs := os.Args
	t.Cleanup(func() { os.Args = previousArgs })
	t.Setenv("OPENARBI_TEST_TOKEN", "")
	os.Args = []string{"openarbi", "--server", "http://localhost:1", "--test-token-env", "legacy"}
	if err := run(); err == nil || err.Error() != `unknown subcommand "legacy"` {
		t.Fatalf("removed command reached authentication or execution: %v", err)
	}
}
