package client

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/zalando/go-keyring"
)

func newTestClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	c, e := New(server.URL, "test-secret", server.Client())
	if e != nil {
		t.Fatal(e)
	}
	return c
}
func TestAuthenticationAndIdempotency(t *testing.T) {
	var requests atomic.Int32
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Header.Get("Authorization") != "Bearer test-secret" || r.Header.Get("X-OpenArbi-Version") != "1" {
			t.Error("missing auth/version")
		}
		var body CreateRequest
		if e := json.NewDecoder(r.Body).Decode(&body); e != nil {
			t.Error(e)
		}
		if body.IdempotencyKey != "same-key" || body.Name != "complete\nprompt" {
			t.Errorf("request changed: %+v", body)
		}
		_ = json.NewEncoder(w).Encode(Room{ID: "accepted", Name: body.Name})
	})
	in := CreateRequest{IdempotencyKey: "same-key", Name: "complete\nprompt"}
	for range 2 {
		out, e := c.CreateRoom(t.Context(), in)
		if e != nil || out.ID != "accepted" {
			t.Fatalf("%+v %v", out, e)
		}
	}
	if requests.Load() != 2 {
		t.Fatalf("unexpected retries: %d", requests.Load())
	}
}
func TestLostAcknowledgementNeverAutomaticallyResubmits(t *testing.T) {
	var calls atomic.Int32
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		_ = conn.Close()
	})
	_, err := c.CreateRoom(t.Context(), CreateRequest{IdempotencyKey: "stable", Name: "one"})
	if _, ok := errors.AsType[*UncertainError](err); !ok {
		t.Fatalf("expected uncertain result: %v", err)
	}
	if strings.Contains(err.Error(), "test-secret") {
		t.Fatal("token leaked")
	}
	if calls.Load() != 1 {
		t.Fatal("mutation replayed")
	}
}
func TestRedirectsDoNotLeakToken(t *testing.T) {
	var leaked atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked.Store(true) }))
	defer target.Close()
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	})
	if _, err := c.Rooms(t.Context()); err == nil {
		t.Fatal("redirect accepted")
	}
	if leaked.Load() {
		t.Fatal("followed redirect")
	}
}
func TestInvalidURLsAndVersion(t *testing.T) {
	for _, base := range []string{"https://user:password@host", "http://example.com", "file:///tmp/socket", "https://host?secret=1"} {
		if _, e := New(base, "token", nil); e == nil {
			t.Errorf("accepted %s", base)
		}
	}
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"version":"999"}`) })
	if _, e := c.Capabilities(t.Context()); e == nil {
		t.Fatal("unsupported protocol accepted")
	}
}
func TestCredentialsOSStoreContract(t *testing.T) {
	keyring.MockInit()
	v := Credentials{}
	if err := v.Set("https://test.invalid", "not-a-real-token"); err != nil {
		t.Fatal(err)
	}
	token, err := v.Get("https://test.invalid")
	if err != nil || token != "not-a-real-token" {
		t.Fatal("credential roundtrip failed")
	}
	if err = v.Delete("https://test.invalid"); err != nil {
		t.Fatal(err)
	}
	if _, err = v.Get("https://test.invalid"); err == nil {
		t.Fatal("deleted token remains")
	}
}

func TestServerErrorMutationIsUncertain(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		fmt.Fprint(w, `{"code":"internal","message":"Request failed"}`)
	})
	_, err := c.CreateRoom(t.Context(), CreateRequest{IdempotencyKey: "stable", Name: "one"})
	if _, ok := errors.AsType[*UncertainError](err); !ok {
		t.Fatalf("expected uncertain server result: %v", err)
	}
}
