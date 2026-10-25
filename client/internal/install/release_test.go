package install

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestResolveVersionedAndLatestRelease(t *testing.T) {
	var base string
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/latest" {
			json.NewEncoder(w).Encode(map[string]any{"tag_name": "v1.2.3", "draft": false})
			return
		}
		if r.URL.Path != "/v1.2.3/release.json" {
			http.NotFound(w, r)
			return
		}
		json.NewEncoder(w).Encode(ReleaseManifest{"v1.2.3", map[string]ReleasePackage{"linux-amd64": {base + "/payload.tar.gz", strings.Repeat("a", 64)}}})
	}))
	defer server.Close()
	base = server.URL
	r := Resolver{Client: server.Client(), Base: base, Latest: base + "/latest"}
	for _, version := range []string{"", "v1.2.3"} {
		got, pkg, err := r.Resolve(t.Context(), version, "linux-amd64")
		if err != nil {
			t.Fatal(err)
		}
		if got != "v1.2.3" || pkg.SHA256 != strings.Repeat("a", 64) {
			t.Fatal("wrong release")
		}
	}
	for _, version := range []string{"../escape", "v9.9.9"} {
		if _, _, err := r.Resolve(t.Context(), version, "linux-amd64"); err == nil {
			t.Fatal("invalid or missing release accepted")
		}
	}
	if _, _, err := r.Resolve(t.Context(), "v1.2.3", "darwin-arm64"); err == nil {
		t.Fatal("missing platform accepted")
	}
}
func TestRejectInvalidManifest(t *testing.T) {
	for _, body := range []string{`{"version":"v2.0.0","packages":{}}`, `{"version":"v1.0.0","packages":{"linux-amd64":{"url":"http://example.com/a","sha256":"` + strings.Repeat("a", 64) + `"}}}`, `{"version":"v1.0.0","packages":{"linux-amd64":{"url":"https://example.com/a","sha256":"fake"}}}`, `{} {}`} {
		t.Run(body[:min(25, len(body))], func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(body)) }))
			defer server.Close()
			if _, _, err := (Resolver{Client: server.Client(), Base: server.URL}).Resolve(t.Context(), "v1.0.0", "linux-amd64"); err == nil {
				t.Fatal("invalid manifest accepted")
			}
		})
	}
}
