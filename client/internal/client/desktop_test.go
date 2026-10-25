package client

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDesktopAllocationWire(t *testing.T) {
	claim := ClaimRequest{DeviceID: "device", SessionID: "session", IdempotencyKey: "stable-key"}
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("Authorization") != "Bearer credential" {
			t.Error("missing credentials")
		}
		switch r.URL.Path {
		case "/v1/capabilities":
			json.NewEncoder(w).Encode(map[string]any{"version": "1", "desktop_allocation": map[string]int{"version": 1, "max_active_rooms": 4}})
		case "/v1/rooms/claim":
			var got ClaimRequest
			if r.Method != "POST" || json.NewDecoder(r.Body).Decode(&got) != nil || got != claim {
				t.Error("incorrect allocation request")
			}
			json.NewEncoder(w).Encode(RoomBinding{DeviceID: "device", SessionID: "session", Room: Room{ID: "room"}, Ready: true})
		case "/v1/rooms/bindings/device/session":
			if r.Method != "GET" {
				t.Error("restore mutation")
			}
			json.NewEncoder(w).Encode(RoomBinding{DeviceID: "device", SessionID: "session", Room: Room{ID: "room"}, Ready: true})
		default:
			t.Error("unexpected route")
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	api, err := New(server.URL, "credential", nil)
	if err != nil {
		t.Fatal(err)
	}
	cap, err := api.DesktopCapabilities(t.Context())
	if err != nil || cap.Version != 1 || cap.MaxActiveRooms != 4 {
		t.Fatalf("capability: %v", err)
	}
	binding, err := api.ClaimRoom(t.Context(), claim)
	if err != nil || binding.Room.ID != "room" {
		t.Fatal(err)
	}
	restored, err := api.RoomBinding(t.Context(), "device", "session")
	if err != nil || restored != binding {
		t.Fatal(err)
	}
	if calls != 3 {
		t.Fatalf("%d requests", calls)
	}
}
func TestMissingDesktopCapabilityFailsExplicitly(t *testing.T) {
	for _, body := range []string{`{"version":"1"}`, `{"version":"1","desktop_allocation":{"version":2,"max_active_rooms":4}}`} {
		t.Run(body, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(body)) }))
			defer server.Close()
			api, _ := New(server.URL, "credential", nil)
			if _, err := api.DesktopCapabilities(t.Context()); err == nil {
				t.Fatal("unsupported server accepted")
			}
		})
	}
}
