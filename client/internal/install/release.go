package install

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const ReleaseBase = "https://github.com/hustLer2k/openarbi-kitty/releases/download"
const LatestRelease = "https://api.github.com/repos/hustLer2k/openarbi-kitty/releases/latest"

type ReleasePackage struct {
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
}
type ReleaseManifest struct {
	Version  string                    `json:"version"`
	Packages map[string]ReleasePackage `json:"packages"`
}
type Resolver struct {
	Client       *http.Client
	Base, Latest string
}

func (r Resolver) get(ctx context.Context, address string, target any) error {
	u, err := url.Parse(address)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return errors.New("release metadata requires HTTPS")
	}
	client := r.Client
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	// Clone so caller clients cannot accidentally allow a downgrade redirect.
	safe := *client
	safe.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if req.URL.Scheme != "https" {
			return errors.New("non-HTTPS release redirect")
		}
		if len(via) >= 10 {
			return errors.New("too many redirects")
		}
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	response, err := safe.Do(req)
	if err != nil {
		return fmt.Errorf("fetch release metadata: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("release metadata HTTP %d; specify an existing published --version", response.StatusCode)
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, 1<<20))
	if err = decoder.Decode(target); err != nil {
		return fmt.Errorf("invalid release metadata: %w", err)
	}
	if decoder.Decode(new(any)) != io.EOF {
		return errors.New("release metadata has trailing or excessive data")
	}
	return nil
}
func (r Resolver) Resolve(ctx context.Context, version, platform string) (string, ReleasePackage, error) {
	switch platform {
	case "linux-amd64", "darwin-arm64":
	default:
		return "", ReleasePackage{}, fmt.Errorf("unsupported release platform %q", platform)
	}
	if version == "" {
		latest := r.Latest
		if latest == "" {
			latest = LatestRelease
		}
		var response struct {
			TagName    string `json:"tag_name"`
			Draft      bool   `json:"draft"`
			Prerelease bool   `json:"prerelease"`
		}
		if err := r.get(ctx, latest, &response); err != nil {
			return "", ReleasePackage{}, err
		}
		if response.Draft || response.Prerelease {
			return "", ReleasePackage{}, errors.New("latest release is not a stable published release")
		}
		version = response.TagName
	}
	if !versionPattern.MatchString(version) {
		return "", ReleasePackage{}, errors.New("release version is not valid semantic version")
	}
	base := r.Base
	if base == "" {
		base = ReleaseBase
	}
	var manifest ReleaseManifest
	if err := r.get(ctx, strings.TrimRight(base, "/")+"/"+version+"/release.json", &manifest); err != nil {
		return "", ReleasePackage{}, err
	}
	if manifest.Version != version {
		return "", ReleasePackage{}, errors.New("release manifest version does not match requested version")
	}
	pkg, ok := manifest.Packages[platform]
	if !ok {
		return "", ReleasePackage{}, fmt.Errorf("release %s has no %s package", version, platform)
	}
	u, err := url.Parse(pkg.URL)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return "", ReleasePackage{}, errors.New("release package URL must use HTTPS")
	}
	digest, err := hex.DecodeString(pkg.SHA256)
	if err != nil || len(digest) != 32 {
		return "", ReleasePackage{}, errors.New("release manifest SHA-256 is invalid")
	}
	return version, pkg, nil
}
