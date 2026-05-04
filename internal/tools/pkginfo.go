package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// PkgInfo queries package registries for metadata: latest version,
// description, license, repository, dependency count.
type PkgInfo struct{}

func (PkgInfo) Name() string { return "pkg_info" }
func (PkgInfo) Description() string {
	return `Look up a package on a public registry. Returns name, latest version, description, license, repository URL, and a dependency-count summary.

Registries:
- npm        (Node)            — registry.npmjs.org
- pypi       (Python)          — pypi.org
- go         (Go modules)      — proxy.golang.org
- crates     (Rust)            — crates.io

For Go modules, pass the full module path (e.g. github.com/foo/bar).`
}
func (PkgInfo) Schema() json.RawMessage {
	return json.RawMessage(`{
"type":"object",
"properties":{
  "registry":{"type":"string","enum":["npm","pypi","go","crates"]},
  "name":{"type":"string","description":"Package name (Go: full module path)."}
},
"required":["registry","name"]
}`)
}
func (PkgInfo) Call(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Registry string `json:"registry"`
		Name     string `json:"name"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", err
	}
	if p.Registry == "" || p.Name == "" {
		return "", errors.New("registry and name are required")
	}

	rctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	switch p.Registry {
	case "npm":
		return pkgInfoNpm(rctx, p.Name)
	case "pypi":
		return pkgInfoPyPI(rctx, p.Name)
	case "go":
		return pkgInfoGo(rctx, p.Name)
	case "crates":
		return pkgInfoCrates(rctx, p.Name)
	default:
		return "", fmt.Errorf("unknown registry: %s", p.Registry)
	}
}

func httpJSON(ctx context.Context, url string, dst any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "ageni/1.0")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return errors.New("not found")
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("registry returned %d: %s", resp.StatusCode, string(body))
	}
	return json.NewDecoder(resp.Body).Decode(dst)
}

func pkgInfoNpm(ctx context.Context, name string) (string, error) {
	var meta struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		License     string `json:"license"`
		DistTags    struct {
			Latest string `json:"latest"`
		} `json:"dist-tags"`
		Repository struct {
			URL string `json:"url"`
		} `json:"repository"`
		Versions map[string]struct {
			Dependencies map[string]string `json:"dependencies"`
		} `json:"versions"`
	}
	if err := httpJSON(ctx, "https://registry.npmjs.org/"+url.PathEscape(name), &meta); err != nil {
		return "", err
	}
	deps := 0
	if v, ok := meta.Versions[meta.DistTags.Latest]; ok {
		deps = len(v.Dependencies)
	}
	return fmt.Sprintf("npm/%s\n  latest:     %s\n  license:    %s\n  repo:       %s\n  deps:       %d\n  desc:       %s",
		meta.Name, meta.DistTags.Latest, meta.License, meta.Repository.URL, deps, meta.Description), nil
}

func pkgInfoPyPI(ctx context.Context, name string) (string, error) {
	var meta struct {
		Info struct {
			Name         string            `json:"name"`
			Version      string            `json:"version"`
			Summary      string            `json:"summary"`
			License      string            `json:"license"`
			HomePage     string            `json:"home_page"`
			ProjectURLs  map[string]string `json:"project_urls"`
			RequiresDist []string          `json:"requires_dist"`
		} `json:"info"`
	}
	if err := httpJSON(ctx, "https://pypi.org/pypi/"+url.PathEscape(name)+"/json", &meta); err != nil {
		return "", err
	}
	repo := meta.Info.HomePage
	if v, ok := meta.Info.ProjectURLs["Source"]; ok && v != "" {
		repo = v
	} else if v, ok := meta.Info.ProjectURLs["Repository"]; ok && v != "" {
		repo = v
	}
	return fmt.Sprintf("pypi/%s\n  latest:     %s\n  license:    %s\n  repo:       %s\n  deps:       %d\n  summary:    %s",
		meta.Info.Name, meta.Info.Version, meta.Info.License, repo, len(meta.Info.RequiresDist), meta.Info.Summary), nil
}

func pkgInfoGo(ctx context.Context, modulePath string) (string, error) {
	// Go module proxy returns plain text, not JSON, for /@latest. Use json.
	var meta struct {
		Version string `json:"Version"`
		Time    string `json:"Time"`
	}
	enc := strings.ReplaceAll(modulePath, "@", "%40")
	if err := httpJSON(ctx, "https://proxy.golang.org/"+enc+"/@latest", &meta); err != nil {
		return "", err
	}
	return fmt.Sprintf("go/%s\n  latest:     %s\n  time:       %s\n  pkg.go.dev: https://pkg.go.dev/%s",
		modulePath, meta.Version, meta.Time, modulePath), nil
}

func pkgInfoCrates(ctx context.Context, name string) (string, error) {
	var meta struct {
		Crate struct {
			Name        string `json:"name"`
			MaxVersion  string `json:"max_version"`
			Description string `json:"description"`
			Repository  string `json:"repository"`
			Homepage    string `json:"homepage"`
		} `json:"crate"`
		Versions []struct {
			Num     string `json:"num"`
			License string `json:"license"`
		} `json:"versions"`
	}
	if err := httpJSON(ctx, "https://crates.io/api/v1/crates/"+url.PathEscape(name), &meta); err != nil {
		return "", err
	}
	license := ""
	if len(meta.Versions) > 0 {
		license = meta.Versions[0].License
	}
	return fmt.Sprintf("crates/%s\n  latest:     %s\n  license:    %s\n  repo:       %s\n  desc:       %s",
		meta.Crate.Name, meta.Crate.MaxVersion, license, meta.Crate.Repository, meta.Crate.Description), nil
}
