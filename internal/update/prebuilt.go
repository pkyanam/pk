package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const (
	officialReleaseRepository = "https://github.com/pkyanam/pk"
	officialReleaseAPI        = "https://api.github.com/repos/pkyanam/pk"
	maxReleaseArchiveBytes    = 256 << 20
	maxExtractedReleaseBytes  = 512 << 20
	maxReleaseArchiveFiles    = 100_000
)

var (
	ErrNoPublishedRelease = errors.New("no published pk release is available")
	ErrNoPlatformRelease  = errors.New("latest pk release has no asset for this platform")
	releaseTagPattern     = regexp.MustCompile(`^v[0-9A-Za-z][0-9A-Za-z.+_-]{0,63}$`)
)

type githubRelease struct {
	TagName string `json:"tag_name"`
	Assets  []struct {
		Name string `json:"name"`
	} `json:"assets"`
}

type archiveLink struct{ name, target string }

// StageLatestPrebuilt downloads and stages a checksum-verified paired pk+UI
// release. It returns alreadyCurrent when the active managed release matches
// the latest published tag, avoiding a large no-op download.
func (manager Manager) StageLatestPrebuilt(ctx context.Context, goos, goarch string) (release Release, alreadyCurrent bool, err error) {
	manager.report("release_check")
	apiBase := strings.TrimRight(manager.ReleaseAPIBaseURL, "/")
	if apiBase == "" {
		apiBase = officialReleaseAPI
	}
	downloadBase := strings.TrimRight(manager.ReleaseDownloadBaseURL, "/")
	if downloadBase == "" {
		downloadBase = officialReleaseRepository
	}
	client := manager.releaseHTTPClient(apiBase, downloadBase)
	var latest githubRelease
	status, err := manager.fetchReleaseJSON(ctx, client, apiBase+"/releases/latest", &latest)
	if err != nil {
		if status == http.StatusNotFound {
			return Release{}, false, ErrNoPublishedRelease
		}
		return Release{}, false, err
	}
	if !releaseTagPattern.MatchString(latest.TagName) {
		return Release{}, false, errors.New("latest pk release has an invalid tag")
	}
	assetName := fmt.Sprintf("pk_%s_%s.tar.gz", goos, goarch)
	hasAsset := false
	for _, asset := range latest.Assets {
		if asset.Name == assetName {
			hasAsset = true
			break
		}
	}
	if !hasAsset {
		return Release{}, false, ErrNoPlatformRelease
	}
	root, err := manager.root()
	if err != nil {
		return Release{}, false, err
	}
	if active, exists, readErr := manager.releaseFromPointer(filepath.Join(root, "current")); readErr != nil {
		return Release{}, false, readErr
	} else if exists && active.GitRepository == "https://github.com/pkyanam/pk.git" && active.GitRef == latest.TagName {
		if err := validateRelease(active); err != nil {
			return Release{}, false, fmt.Errorf("active release matching latest tag is invalid: %w", err)
		}
		return active, true, nil
	}

	manager.report("release_download")
	tagPath := url.PathEscape(latest.TagName)
	assetURL := downloadBase + "/releases/download/" + tagPath + "/" + assetName
	checksumURL := downloadBase + "/releases/download/" + tagPath + "/SHA256SUMS"
	checksums, _, err := manager.fetchBytes(ctx, client, checksumURL, 64<<10)
	if err != nil {
		return Release{}, false, fmt.Errorf("download release checksums: %w", err)
	}
	wantHash, err := checksumForAsset(checksums, assetName)
	if err != nil {
		return Release{}, false, err
	}
	archive, _, err := manager.fetchBytes(ctx, client, assetURL, maxReleaseArchiveBytes)
	if err != nil {
		return Release{}, false, fmt.Errorf("download release archive: %w", err)
	}
	archiveSum := sha256.Sum256(archive)
	archiveHash := hex.EncodeToString(archiveSum[:])
	if archiveHash != wantHash {
		return Release{}, false, errors.New("pk release archive checksum mismatch")
	}
	release, err = manager.StageReleaseArchive(ctx, archive, latest.TagName, archiveHash)
	if err != nil {
		return Release{}, false, err
	}
	return release, false, nil
}

// StageReleaseArchive imports a paired archive only when the caller's expected
// SHA-256 matches. It is also used by the bootstrap installer after it verifies
// the release checksum list.
func (manager Manager) StageReleaseArchive(ctx context.Context, archive []byte, tag, wantHash string) (Release, error) {
	if err := ctx.Err(); err != nil {
		return Release{}, err
	}
	if !releaseTagPattern.MatchString(tag) {
		return Release{}, errors.New("release archive has an invalid tag")
	}
	if len(archive) == 0 || len(archive) > maxReleaseArchiveBytes || len(wantHash) != 64 {
		return Release{}, errors.New("release archive is missing or exceeds its size limit")
	}
	if _, err := hex.DecodeString(wantHash); err != nil {
		return Release{}, errors.New("release archive checksum is invalid")
	}
	archiveSum := sha256.Sum256(archive)
	archiveHash := hex.EncodeToString(archiveSum[:])
	if archiveHash != strings.ToLower(wantHash) {
		return Release{}, errors.New("pk release archive checksum mismatch")
	}
	manager.report("release_verify")
	stageDir, err := os.MkdirTemp("", "pk-release-archive-")
	if err != nil {
		return Release{}, err
	}
	defer os.RemoveAll(stageDir)
	if err := extractReleaseArchive(archive, stageDir); err != nil {
		return Release{}, fmt.Errorf("invalid pk release archive: %w", err)
	}
	if _, err := os.Stat(filepath.Join(stageDir, "ui", "package.json")); err != nil {
		return Release{}, errors.New("pk release archive is missing its UI package metadata")
	}
	if _, err := os.Stat(filepath.Join(stageDir, "ui", "bun.lock")); err != nil {
		return Release{}, errors.New("pk release archive is missing its UI lockfile")
	}
	provenance := &sourceProvenance{
		source: "https://github.com/pkyanam/pk", repository: "https://github.com/pkyanam/pk.git",
		ref: tag, revision: tag, dirtyKnown: true, distributionHash: archiveHash,
	}
	release, err := manager.importArtifacts(filepath.Join(stageDir, "pk"), filepath.Join(stageDir, "ui"), provenance)
	if err != nil {
		return Release{}, fmt.Errorf("stage verified pk release: %w", err)
	}
	return release, nil
}

func (manager Manager) releaseHTTPClient(apiBase, downloadBase string) *http.Client {
	if manager.HTTPClient != nil {
		return manager.HTTPClient
	}
	allowed := map[string]bool{}
	for _, raw := range []string{apiBase, downloadBase} {
		if parsed, err := url.Parse(raw); err == nil {
			allowed[strings.ToLower(parsed.Host)] = true
		}
	}
	allowed["release-assets.githubusercontent.com"] = true
	allowed["objects.githubusercontent.com"] = true
	return &http.Client{Timeout: 45 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 6 || req.URL.Scheme != "https" || !allowed[strings.ToLower(req.URL.Host)] {
			return errors.New("release download redirected to an untrusted host")
		}
		return nil
	}}
}

func (manager Manager) fetchReleaseJSON(ctx context.Context, client *http.Client, endpoint string, target any) (int, error) {
	data, status, err := manager.fetchBytes(ctx, client, endpoint, 1<<20)
	if err != nil {
		return status, err
	}
	if err := json.Unmarshal(data, target); err != nil {
		return status, errors.New("latest pk release metadata is invalid")
	}
	return status, nil
}

func (manager Manager) fetchBytes(ctx context.Context, client *http.Client, endpoint string, limit int64) ([]byte, int, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, 0, err
	}
	request.Header.Set("User-Agent", "pk-updater")
	request.Header.Set("Accept", "application/vnd.github+json, application/octet-stream")
	response, err := client.Do(request)
	if err != nil {
		return nil, 0, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, response.StatusCode, fmt.Errorf("GitHub returned HTTP %d", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return nil, response.StatusCode, err
	}
	if int64(len(data)) > limit {
		return nil, response.StatusCode, fmt.Errorf("GitHub release response exceeds %d bytes", limit)
	}
	return data, response.StatusCode, nil
}

func checksumForAsset(data []byte, name string) (string, error) {
	lines := bytes.Split(data, []byte{'\n'})
	found := ""
	for _, line := range lines {
		fields := strings.Fields(string(line))
		if len(fields) != 2 {
			continue
		}
		filename := strings.TrimPrefix(fields[1], "*")
		if filename != name {
			continue
		}
		if found != "" || len(fields[0]) != 64 {
			return "", errors.New("release checksum list has an invalid or duplicate asset entry")
		}
		if _, err := hex.DecodeString(fields[0]); err != nil {
			return "", errors.New("release checksum list has an invalid SHA-256 value")
		}
		found = strings.ToLower(fields[0])
	}
	if found == "" {
		return "", fmt.Errorf("release checksum list is missing %s", name)
	}
	return found, nil
}

func extractReleaseArchive(data []byte, destination string) error {
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return err
	}
	defer gz.Close()
	reader := tar.NewReader(gz)
	seen := make(map[string]bool)
	var links []archiveLink
	var total int64
	count := 0
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		count++
		if count > maxReleaseArchiveFiles {
			return errors.New("archive contains too many entries")
		}
		name := strings.TrimSuffix(header.Name, "/")
		if strings.Contains(name, "\\") || strings.HasPrefix(name, "/") || name == "" {
			return errors.New("archive contains an unsafe path")
		}
		clean := path.Clean(name)
		if clean != name || clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
			return errors.New("archive contains a traversal path")
		}
		if clean != "pk" && clean != "ui" && !strings.HasPrefix(clean, "ui/") {
			return fmt.Errorf("archive contains unexpected path %q", clean)
		}
		if seen[clean] {
			return fmt.Errorf("archive contains duplicate path %q", clean)
		}
		seen[clean] = true
		target := filepath.Join(destination, filepath.FromSlash(clean))
		switch header.Typeflag {
		case tar.TypeDir:
			if clean == "pk" || strings.HasPrefix(clean, "pk/") {
				return errors.New("pk archive entry must be a regular file")
			}
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeRegA:
			if header.Size < 0 || header.Size > 100<<20 || total > maxExtractedReleaseBytes-header.Size {
				return errors.New("archive file size exceeds the extraction limit")
			}
			total += header.Size
			if err := archiveParents(destination, filepath.Dir(target)); err != nil {
				return err
			}
			mode := os.FileMode(0o644)
			if header.Mode&0o111 != 0 {
				mode = 0o755
			}
			file, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
			if err != nil {
				return err
			}
			_, copyErr := io.CopyN(file, reader, header.Size)
			closeErr := file.Close()
			if copyErr != nil || closeErr != nil {
				return errors.Join(copyErr, closeErr)
			}
		case tar.TypeSymlink:
			if !strings.HasPrefix(clean, "ui/") || filepath.IsAbs(header.Linkname) || strings.Contains(header.Linkname, "\\") {
				return errors.New("archive contains an unsafe symbolic link")
			}
			resolved := path.Clean(path.Join(path.Dir(clean), header.Linkname))
			if resolved == "ui" || !strings.HasPrefix(resolved, "ui/") || strings.HasPrefix(resolved, "../") {
				return errors.New("archive symbolic link escapes UI directory")
			}
			links = append(links, archiveLink{name: clean, target: header.Linkname})
		default:
			return fmt.Errorf("archive contains unsupported entry type %d", header.Typeflag)
		}
	}
	for _, link := range links {
		target := filepath.Join(destination, filepath.FromSlash(link.name))
		if err := archiveParents(destination, filepath.Dir(target)); err != nil {
			return err
		}
		if err := os.Symlink(link.target, target); err != nil {
			return err
		}
	}
	if !seen["pk"] || !seen["ui/dist/main.js"] || !seen["ui/node_modules"] || !seen["ui/package.json"] || !seen["ui/bun.lock"] {
		return errors.New("archive is missing required paired binary or UI files")
	}
	info, err := os.Stat(filepath.Join(destination, "pk"))
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return errors.New("archive pk binary is not executable")
	}
	return nil
}

func archiveParents(root, directory string) error {
	rel, err := filepath.Rel(root, directory)
	if err != nil || filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return errors.New("archive directory escaped staging root")
	}
	current := root
	if rel == "." {
		return nil
	}
	for _, component := range strings.Split(rel, string(filepath.Separator)) {
		current = filepath.Join(current, component)
		if err := os.Mkdir(current, 0o755); err != nil && !errors.Is(err, os.ErrExist) {
			return err
		}
		info, err := os.Lstat(current)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("archive path parent is not a real directory")
		}
	}
	return nil
}
