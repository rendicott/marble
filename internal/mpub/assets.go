package mpub

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const (
	// AssetsDir is the per-slug subdirectory holding uploaded assets.
	AssetsDir = "assets"
	// MaxAssetBytes caps a single asset.
	MaxAssetBytes = 10 << 20 // 10 MiB
	// MaxAssetsPerPage caps how many assets one page may hold.
	MaxAssetsPerPage = 50
	// MaxAssetTotalBytes caps the total size of a page's assets.
	MaxAssetTotalBytes = 32 << 20 // 32 MiB
)

// Asset is a file published beside a page and served at /mpub/{slug}/{name}.
type Asset struct {
	Name string
	Data []byte
}

// AssetInfo describes a stored asset.
type AssetInfo struct {
	Name        string `json:"name"`
	Bytes       int64  `json:"bytes"`
	ContentType string `json:"content_type"`
}

// assetTypes is the allowlist of served asset extensions. Images only: pages are
// served under a locked-down CSP, so scripts/styles/fonts would not run anyway.
var assetTypes = map[string]string{
	".png":  "image/png",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".gif":  "image/gif",
	".webp": "image/webp",
	".avif": "image/avif",
	".svg":  "image/svg+xml",
}

var assetNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,99}$`)

// AssetContentType returns the served content type for an asset file name.
func AssetContentType(name string) (string, bool) {
	ct, ok := assetTypes[strings.ToLower(filepath.Ext(name))]
	return ct, ok
}

// ValidateAssetName checks a flat asset file name (no directories).
func ValidateAssetName(name string) error {
	if !assetNameRE.MatchString(name) || strings.Contains(name, "..") {
		return fmt.Errorf("asset name %q invalid: use 1-100 chars of [A-Za-z0-9._-] starting with a letter or digit (rename the file, e.g. no spaces)", name)
	}
	if _, ok := AssetContentType(name); !ok {
		return fmt.Errorf("asset %q: unsupported type (allowed: png, jpg, jpeg, gif, webp, avif, svg)", name)
	}
	return nil
}

// ValidateAssets checks names, uniqueness, and size limits for one publish call.
func ValidateAssets(assets []Asset) error {
	if len(assets) > MaxAssetsPerPage {
		return fmt.Errorf("too many assets (%d, max %d)", len(assets), MaxAssetsPerPage)
	}
	seen := make(map[string]bool, len(assets))
	var total int
	for _, a := range assets {
		if err := ValidateAssetName(a.Name); err != nil {
			return err
		}
		if seen[a.Name] {
			return fmt.Errorf("duplicate asset name %q", a.Name)
		}
		seen[a.Name] = true
		if len(a.Data) > MaxAssetBytes {
			return fmt.Errorf("asset %q exceeds max size %d bytes", a.Name, MaxAssetBytes)
		}
		total += len(a.Data)
	}
	if total > MaxAssetTotalBytes {
		return fmt.Errorf("assets exceed max total size %d bytes", MaxAssetTotalBytes)
	}
	return nil
}

func (s *Store) writeAssets(dir string, assets []Asset) error {
	if len(assets) == 0 {
		return nil
	}
	adir := filepath.Join(dir, AssetsDir)
	if err := os.MkdirAll(adir, 0o755); err != nil {
		return err
	}
	// The per-call checks in ValidateAssets do not see assets already stored;
	// enforce the per-page caps across old + new.
	existing := map[string]int64{}
	if ents, err := os.ReadDir(adir); err == nil {
		for _, e := range ents {
			if info, err := e.Info(); err == nil && !e.IsDir() {
				existing[e.Name()] = info.Size()
			}
		}
	}
	for _, a := range assets {
		existing[a.Name] = int64(len(a.Data))
	}
	var total int64
	for _, n := range existing {
		total += n
	}
	if len(existing) > MaxAssetsPerPage {
		return fmt.Errorf("page would hold %d assets (max %d)", len(existing), MaxAssetsPerPage)
	}
	if total > MaxAssetTotalBytes {
		return fmt.Errorf("page assets would total %d bytes (max %d)", total, MaxAssetTotalBytes)
	}
	for _, a := range assets {
		if err := os.WriteFile(filepath.Join(adir, a.Name), a.Data, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// ListAssets returns a slug's assets sorted by name.
func (s *Store) ListAssets(slug string) ([]AssetInfo, error) {
	dir, err := s.slugDir(slug)
	if err != nil {
		return nil, err
	}
	ents, err := os.ReadDir(filepath.Join(dir, AssetsDir))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []AssetInfo
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		ct, ok := AssetContentType(e.Name())
		if !ok {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, AssetInfo{Name: e.Name(), Bytes: info.Size(), ContentType: ct})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// ReadAsset loads one asset's bytes and content type.
func (s *Store) ReadAsset(slug, name string) ([]byte, string, error) {
	dir, err := s.slugDir(slug)
	if err != nil {
		return nil, "", err
	}
	if err := ValidateAssetName(name); err != nil {
		return nil, "", err
	}
	ct, _ := AssetContentType(name)
	data, err := os.ReadFile(filepath.Join(dir, AssetsDir, name))
	if err != nil {
		return nil, "", err
	}
	return data, ct, nil
}

var refAttrRE = regexp.MustCompile(`(?i)(\b(?:src|href|poster)\s*=\s*)(?:"([^"]*)"|'([^']*)')`)

// RewriteAssetRefs points relative src/href/poster attributes that name a stored
// asset at /mpub/{slug}/{name}. Page URLs have no trailing slash, so a bare
// relative reference would otherwise resolve to /mpub/{name}. Unknown names are
// left untouched (Lint reports them at publish time).
func RewriteAssetRefs(html, slug string, assets []AssetInfo) string {
	if len(assets) == 0 {
		return html
	}
	known := make(map[string]bool, len(assets))
	for _, a := range assets {
		known[a.Name] = true
	}
	return refAttrRE.ReplaceAllStringFunc(html, func(m string) string {
		sub := refAttrRE.FindStringSubmatch(m)
		val, quote := sub[2], `"`
		if sub[3] != "" {
			val, quote = sub[3], `'`
		}
		name := strings.TrimPrefix(val, "./")
		if !known[name] {
			return m
		}
		return sub[1] + quote + "/mpub/" + slug + "/" + name + quote
	})
}

var (
	mdImageRE     = regexp.MustCompile(`!\[[^\]]*\]\(([^)\s]+)`)
	placeholderRE = regexp.MustCompile(`__[A-Z][A-Z0-9]*(?:_[A-Z0-9]+)*__|\{\{\s*[A-Za-z_][A-Za-z0-9_.]*\s*\}\}|%%[A-Z][A-Z0-9_]*%%`)
	schemeRE      = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9+.-]*:`)
)

// Lint returns non-fatal warnings about content that will likely render wrong:
// relative image references with no matching asset, and leftover placeholders.
// assets is the page's full asset set after publish.
func Lint(contentType, content string, assets []AssetInfo) []string {
	have := make(map[string]bool, len(assets))
	for _, a := range assets {
		have[a.Name] = true
	}
	var warnings []string
	seen := map[string]bool{}
	check := func(ref string) {
		ref = strings.TrimSpace(ref)
		if ref == "" || strings.HasPrefix(ref, "/") || strings.HasPrefix(ref, "#") || schemeRE.MatchString(ref) {
			return
		}
		name := strings.TrimPrefix(ref, "./")
		if have[name] || seen[ref] {
			return
		}
		seen[ref] = true
		warnings = append(warnings, fmt.Sprintf("relative reference %q matches no published asset — pass the file via assets=[...] or use an absolute/https URL", ref))
	}
	switch normalizeContentType(contentType) {
	case "text/html":
		for _, m := range refAttrRE.FindAllStringSubmatch(content, -1) {
			// Only src/poster are always media; a relative href may be a page link,
			// so restrict href checks to image-looking targets.
			attr := strings.ToLower(strings.TrimSpace(strings.TrimRight(strings.TrimSpace(m[1]), "= \t")))
			ref := m[2]
			if ref == "" {
				ref = m[3]
			}
			if attr == "href" {
				if _, ok := AssetContentType(ref); !ok {
					continue
				}
			}
			check(ref)
		}
	case "text/markdown":
		for _, m := range mdImageRE.FindAllStringSubmatch(content, -1) {
			check(m[1])
		}
	}
	var ph []string
	phSeen := map[string]bool{}
	for _, m := range placeholderRE.FindAllString(content, -1) {
		if !phSeen[m] {
			phSeen[m] = true
			ph = append(ph, m)
		}
	}
	if len(ph) > 0 {
		if len(ph) > 5 {
			ph = ph[:5]
		}
		warnings = append(warnings, fmt.Sprintf("content contains what look like unreplaced placeholders: %s", strings.Join(ph, ", ")))
	}
	return warnings
}
