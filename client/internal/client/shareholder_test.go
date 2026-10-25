package client

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func localPublication() PublishRequest {
	return PublishRequest{SourceKind: "local", SessionID: "session", ThreadID: "thread", TurnID: "turn", Kind: "final", Text: "Done", ParseMode: "HTML"}
}
func TestPublishShareholderAuthenticatedAndSingleAttempt(t *testing.T) {
	for _, reply := range []string{`{"status":"sent","message_id":42,"chat_id":-123}`, `{"status":"suppressed","message_id":42,"chat_id":-123}`, `{"status":"unknown"}`, `not-json`} {
		t.Run(reply, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.URL.Path != "/v1/shareholder/updates" || r.Method != "POST" || r.Header.Get("Authorization") != "Bearer fixture-token" {
					t.Error("incorrect authenticated publishing route")
				}
				var request PublishRequest
				if json.NewDecoder(r.Body).Decode(&request) != nil || request != localPublication() {
					t.Error("publication changed")
				}
				_, _ = w.Write([]byte(reply))
			}))
			defer server.Close()
			api, _ := New(server.URL, "fixture-token", server.Client())
			out, err := api.PublishShareholder(t.Context(), localPublication())
			if strings.Contains(reply, `"message_id"`) {
				if err != nil || out.MessageID != 42 {
					t.Fatal(out, err)
				}
			} else {
				if _, ok := errors.AsType[*UncertainError](err); !ok {
					t.Fatal("invalid acknowledgement must remain uncertain", err)
				}
			}
			if calls.Load() != 1 {
				t.Fatal("publication was repeated")
			}
		})
	}
}
func TestPublishShareholderRejectsRoomSourcesLocally(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("invalid source contacted API") }))
	defer server.Close()
	api, _ := New(server.URL, "fixture-token", server.Client())
	request := localPublication()
	request.SourceKind = "room"
	if _, err := api.PublishShareholder(t.Context(), request); err == nil {
		t.Fatal("room source accepted by user CLI client")
	}
}
