package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hustLer2k/openarbi-kitty/client/internal/client"
)

const publicationJSON = `{"source_kind":"local","session_id":"session","thread_id":"thread","turn_id":"turn","kind":"final","text":"Done","parse_mode":"HTML"}`

type fakePublisher struct {
	calls int
	err   error
}

func TestShareholderOnlyKnownPredispatchErrorsAreRetryable(t *testing.T) {
	unauthorized := &client.ResponseError{Status: 401, API: &client.APIError{Code: "unauthorized", Message: "fixture rejection"}}
	for _, test := range []struct {
		name string
		err  error
		safe bool
	}{
		{"authentication", unauthorized, true},
		{"forbidden", &client.ResponseError{Status: 403, API: &client.APIError{Code: "forbidden", Message: "fixture rejection"}}, true},
		{"server-error", &client.ResponseError{Status: 503, API: &client.APIError{Code: "unavailable", Message: "fixture failure"}}, false},
		{"uncertain", &client.UncertainError{Cause: unauthorized}, false},
		{"unconfirmed", errors.New("fixture disconnect"), false},
	} {
		t.Run(test.name, func(t *testing.T) {
			api := &fakePublisher{err: test.err}
			var output bytes.Buffer
			err := runShareholder(t.Context(), api, []string{"publish"}, strings.NewReader(publicationJSON), &output)
			_, safe := errors.AsType[*shareholderNotDispatchedError](err)
			if safe != test.safe || api.calls != 1 || output.Len() != 0 {
				t.Fatal("incorrect retry classification", safe, test.safe, err)
			}
		})
	}
	var output bytes.Buffer
	err := runShareholder(t.Context(), &fakePublisher{}, []string{"publish"}, strings.NewReader(`{`), &output)
	if _, safe := errors.AsType[*shareholderNotDispatchedError](err); !safe {
		t.Fatal("invalid local input must be known predispatch", err)
	}
	var contract struct {
		Error     string `json:"error"`
		Retryable bool   `json:"retryable"`
	}
	if json.Unmarshal([]byte(shareholderNotDispatchedJSON), &contract) != nil || contract.Error != "shareholder_not_dispatched" || !contract.Retryable {
		t.Fatal("machine-readable retry contract changed")
	}
}

func TestShareholderPreflightFailuresAreKnownPredispatch(t *testing.T) {
	previousArgs := os.Args
	t.Cleanup(func() { os.Args = previousArgs })
	t.Setenv("OPENARBI_TEST_TOKEN", "")
	for _, origin := range []string{"invalid://api", "http://localhost:1"} {
		os.Args = []string{"openarbi", "--server", origin, "--test-token-env", "shareholder", "publish"}
		if err := run(); err == nil {
			t.Fatal("invalid preflight succeeded")
		} else if _, safe := errors.AsType[*shareholderNotDispatchedError](err); !safe {
			t.Fatal("login or configuration failure lost its predispatch classification", err)
		}
	}
}

func (f *fakePublisher) PublishShareholder(context.Context, client.PublishRequest) (client.PublishResponse, error) {
	f.calls++
	return client.PublishResponse{Status: "sent", MessageID: 42, ChatID: -123}, f.err
}
func TestShareholderCommandBoundsAndJSON(t *testing.T) {
	for _, input := range []string{publicationJSON, publicationJSON + " {}", `{"extra":true}`, `null`, `{`, strings.Repeat("x", maxShareholderInput+1)} {
		var output bytes.Buffer
		api := &fakePublisher{}
		err := runShareholder(t.Context(), api, []string{"publish"}, strings.NewReader(input), &output)
		if input == publicationJSON {
			if err != nil || api.calls != 1 || !strings.Contains(output.String(), `"message_id":42`) {
				t.Fatal(output.String(), err)
			}
		} else if err == nil || api.calls != 0 || output.Len() != 0 {
			t.Fatal("invalid input contacted server or emitted success", err)
		}
	}
	api := &fakePublisher{err: errors.New("unavailable")}
	var output bytes.Buffer
	if err := runShareholder(t.Context(), api, []string{"publish"}, strings.NewReader(publicationJSON), &output); err == nil || output.Len() != 0 {
		t.Fatal("failed publication printed success")
	}
	if err := runShareholder(t.Context(), api, nil, strings.NewReader(publicationJSON), &output); err == nil {
		t.Fatal("missing action accepted")
	}
}
func TestShareholderMainUsesExistingLoginPath(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/v1/shareholder/updates" || r.Header.Get("Authorization") != "Bearer fixture-token" {
			t.Error("wrong API route or login")
		}
		_ = json.NewEncoder(w).Encode(client.PublishResponse{Status: "suppressed", MessageID: 42, ChatID: -123})
	}))
	defer server.Close()
	previousArgs, previousIn, previousOut := os.Args, os.Stdin, os.Stdout
	t.Cleanup(func() { os.Args, os.Stdin, os.Stdout = previousArgs, previousIn, previousOut })
	dir := t.TempDir()
	inputPath := filepath.Join(dir, "input.json")
	outputPath := filepath.Join(dir, "output.json")
	if err := os.WriteFile(inputPath, []byte(publicationJSON), 0600); err != nil {
		t.Fatal(err)
	}
	in, err := os.Open(inputPath)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	out, err := os.Create(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	os.Stdin, os.Stdout = in, out
	os.Args = []string{"openarbi", "--server", server.URL, "--test-token-env", "shareholder", "publish"}
	t.Setenv("OPENARBI_TEST_TOKEN", "fixture-token")
	if err := run(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(outputPath)
	if err != nil || !strings.Contains(string(data), `"status":"suppressed"`) {
		t.Fatal(string(data), err)
	}
}
