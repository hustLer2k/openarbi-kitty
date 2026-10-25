package client

import (
	"encoding/json"
	"net/http"
	"os"
	"reflect"
	"testing"
)

// These literal, synthetic fixtures also serve as backend contract oracles.
// Round-tripping catches lost/renamed fields without importing backend packages.
func TestWireFixtures(t *testing.T) {
	for _, test := range []struct {
		name  string
		value any
	}{
		{"capabilities", new(Capabilities)}, {"room", new(Room)},
		{"create", new(CreateRequest)}, {"member", new(Member)},
		{"attach", new(AttachInfo)}, {"publication", new(PublishRequest)},
		{"publication_response", new(PublishResponse)}, {"error", new(APIError)},
	} {
		t.Run(test.name, func(t *testing.T) {
			original, err := os.ReadFile("testdata/" + test.name + ".json")
			if err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(original, test.value); err != nil {
				t.Fatal(err)
			}
			encoded, err := json.Marshal(test.value)
			if err != nil {
				t.Fatal(err)
			}
			var want, got any
			if err := json.Unmarshal(original, &want); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(encoded, &got); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(want, got) {
				t.Fatalf("wire contract changed: %s", encoded)
			}
		})
	}
}

func TestNativeRoomEndpoints(t *testing.T) {
	for _, test := range []struct {
		name, method, path, fixture, body string
		call                              func(*Client) error
	}{
		{"get", "GET", "/v1/rooms/fixture-room", "room", "", func(c *Client) error { _, err := c.Room(t.Context(), "fixture-room"); return err }},
		{"attach", "POST", "/v1/rooms/fixture-room/attach", "attach", "{}", func(c *Client) error { _, err := c.AttachRoom(t.Context(), "fixture-room"); return err }},
		{"stop", "POST", "/v1/rooms/fixture-room/stop", "room", "{}", func(c *Client) error { _, err := c.StopRoom(t.Context(), "fixture-room"); return err }},
		{"resume", "POST", "/v1/rooms/fixture-room/resume", "room", "{}", func(c *Client) error { _, err := c.ResumeRoom(t.Context(), "fixture-room"); return err }},
		{"members", "GET", "/v1/rooms/fixture-room/members", "member", "", func(c *Client) error { _, err := c.RoomMembers(t.Context(), "fixture-room"); return err }},
		{"add", "POST", "/v1/rooms/fixture-room/members", "member", `{"identity_id":"fixture-member"}`, func(c *Client) error {
			_, err := c.AddRoomMember(t.Context(), "fixture-room", "fixture-member")
			return err
		}},
		{"remove", "DELETE", "/v1/rooms/fixture-room/members/fixture-member", "member", "", func(c *Client) error {
			_, err := c.RemoveRoomMember(t.Context(), "fixture-room", "fixture-member")
			return err
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != test.method || r.URL.Path != test.path {
					t.Errorf("route changed: %s %s", r.Method, r.URL.Path)
				}
				if r.Header.Get("Authorization") != "Bearer test-secret" || r.Header.Get("X-OpenArbi-Version") != ProtocolVersion {
					t.Error("missing authentication or protocol version")
				}
				if test.body != "" {
					var body any
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
					}
					encoded, err := json.Marshal(body)
					if err != nil || string(encoded) != test.body {
						t.Error("request shape changed")
					}
				}
				payload, err := os.ReadFile("testdata/" + test.fixture + ".json")
				if err != nil {
					t.Error(err)
					http.Error(w, "fixture unavailable", 500)
					return
				}
				if test.fixture == "member" {
					payload = append(append([]byte("["), payload...), ']')
				}
				_, _ = w.Write(payload)
			})
			if err := test.call(c); err != nil {
				t.Fatal(err)
			}
			if calls != 1 {
				t.Fatalf("expected one request, got %d", calls)
			}
		})
	}
}
