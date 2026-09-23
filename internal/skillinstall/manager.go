// Package skillinstall discovers and manages explicitly selected, public
// Agent Skills from GitHub repositories. It copies data files only; it never
// executes skill content.
package skillinstall

import (
	"context"
	"crypto/rand"
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
	"sort"
	"strings"
	"time"
)

const (
	maxTreeResponse = 5 << 20
	maxFileBytes    = 1 << 20
	maxInstallBytes = 2 << 20
	maxFiles        = 128
	maxCandidates   = 64
)

var safeName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,79}$`)

type Manager struct {
	Root   string
	Client *http.Client
}

type Candidate struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Source      string `json:"source"`
	Path        string `json:"path"`
	URL         string `json:"url"`
}

type Installed struct {
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Source      string    `json:"source"`
	Path        string    `json:"path"`
	InstalledAt time.Time `json:"installed_at"`
}

type SearchResult struct {
	Name     string `json:"name"`
	ID       string `json:"id"`
	Source   string `json:"source"`
	Installs int    `json:"installs"`
	URL      string `json:"url"`
}

type sourceRef struct{ owner, repo, ref, wanted string }
type githubRepo struct {
	DefaultBranch string `json:"default_branch"`
}
type githubTree struct {
	Truncated bool `json:"truncated"`
	Tree      []struct {
		Path string `json:"path"`
		Type string `json:"type"`
		Mode string `json:"mode"`
	} `json:"tree"`
}
type marker struct {
	Installed Installed `json:"installed"`
	Files     []string  `json:"files"`
}

func (m Manager) client() *http.Client {
	base := &http.Client{Timeout: 12 * time.Second}
	if m.Client != nil {
		copy := *m.Client
		base = &copy
	}
	priorRedirect := base.CheckRedirect
	base.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 3 || (len(via) > 0 && !strings.EqualFold(req.URL.Host, via[0].URL.Host)) {
			return http.ErrUseLastResponse
		}
		if priorRedirect != nil {
			if err := priorRedirect(req, via); err != nil {
				return err
			}
			if len(via) > 0 && !strings.EqualFold(req.URL.Host, via[0].URL.Host) {
				return http.ErrUseLastResponse
			}
		}
		return nil
	}
	if base.Timeout == 0 || base.Timeout > 12*time.Second {
		base.Timeout = 12 * time.Second
	}
	return base
}

// Discover lists candidate SKILL.md files from owner/repo, a GitHub URL, or
// a skills.sh/<owner>/<repo>/<skill> URL. A skills.sh slug filters the
// repository candidates; skills.sh's catalog API itself requires Vercel OIDC.
func (m Manager) Discover(ctx context.Context, source string) ([]Candidate, error) {
	var cancel context.CancelFunc
	ctx, cancel = boundedContext(ctx, 40*time.Second)
	defer cancel()
	s, err := parseSource(source)
	if err != nil {
		return nil, err
	}
	client := m.client()
	var repo githubRepo
	if err := m.getJSON(ctx, client, "https://api.github.com/repos/"+url.PathEscape(s.owner)+"/"+url.PathEscape(s.repo), maxTreeResponse, &repo); err != nil {
		return nil, err
	}
	ref := s.ref
	if ref == "" {
		ref = repo.DefaultBranch
	}
	if ref == "" {
		return nil, errors.New("GitHub repository did not report a default branch")
	}
	var tree githubTree
	endpoint := "https://api.github.com/repos/" + url.PathEscape(s.owner) + "/" + url.PathEscape(s.repo) + "/git/trees/" + url.PathEscape(ref) + "?recursive=1"
	if err := m.getJSON(ctx, client, endpoint, maxTreeResponse, &tree); err != nil {
		return nil, err
	}
	if tree.Truncated {
		return nil, errors.New("repository tree is too large to browse safely; use a smaller skills repository")
	}
	byDir := map[string]bool{}
	for _, entry := range tree.Tree {
		if entry.Type == "blob" && entry.Mode != "120000" && path.Base(entry.Path) == "SKILL.md" && safeRelative(entry.Path) {
			byDir[path.Dir(entry.Path)] = true
		}
	}
	dirs := make([]string, 0, len(byDir))
	for dir := range byDir {
		dirs = append(dirs, dir)
	}
	sort.Strings(dirs)
	if len(dirs) == 0 {
		return nil, errors.New("repository contains no SKILL.md files; choose a repository with Agent Skills")
	}
	if s.wanted != "" {
		matched := make([]string, 0, 1)
		for _, dir := range dirs {
			if dir == "." || strings.EqualFold(path.Base(dir), s.wanted) {
				matched = append(matched, dir)
			}
		}
		if len(matched) > 0 {
			dirs = matched
		} else if len(dirs) > maxCandidates {
			return nil, fmt.Errorf("selected skill %q does not match a skill folder in %s/%s; reopen the specific skills.sh result or use its GitHub skill URL", s.wanted, s.owner, s.repo)
		}
	}
	if len(dirs) > maxCandidates {
		return nil, fmt.Errorf("repository has more than %d candidate skills; narrow the source to one skills.sh URL", maxCandidates)
	}
	out := make([]Candidate, 0, len(dirs))
	inspectedBytes := 0
	invalidSkillFiles := 0
	for _, dir := range dirs {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		mdPath := path.Join(dir, "SKILL.md")
		body, err := m.getBytes(ctx, client, rawURL(s, ref, mdPath), maxFileBytes)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", mdPath, err)
		}
		inspectedBytes += len(body)
		if inspectedBytes > maxTreeResponse {
			return nil, errors.New("skill metadata exceeds repository discovery byte limit")
		}
		name, description, err := parseSkill(body)
		if err != nil {
			invalidSkillFiles++
			continue
		}
		if s.wanted != "" && !strings.EqualFold(s.wanted, path.Base(dir)) && !strings.EqualFold(s.wanted, name) {
			continue
		}
		// Candidate.Source must round-trip through parseSource during Install.
		// The compact owner/repo@value syntax intentionally means a skill
		// selector, so encode the ref in an explicit GitHub tree URL here.
		baseURL := "https://github.com/" + s.owner + "/" + s.repo + "/tree/" + escapePath(ref)
		candidateURL := baseURL
		if dir != "." {
			candidateURL += "/" + escapePath(dir)
		}
		out = append(out, Candidate{Name: name, Description: description, Source: candidateURL, Path: dir, URL: candidateURL})
	}
	if s.wanted != "" && len(out) == 0 {
		return nil, fmt.Errorf("skill %q was not found in %s/%s", s.wanted, s.owner, s.repo)
	}
	if len(out) == 0 && invalidSkillFiles == len(dirs) {
		return nil, errors.New("repository has SKILL.md files, but none contain valid Agent Skills name and description frontmatter")
	}
	return out, nil
}

// Search uses the unauthenticated endpoint currently called by the official
// skills CLI. skills.sh documents a separate v1 API that requires Vercel OIDC;
// this endpoint is therefore isolated and allowed to fail without blocking
// direct GitHub source discovery.
func (m Manager) Search(ctx context.Context, query string) ([]SearchResult, error) {
	var cancel context.CancelFunc
	ctx, cancel = boundedContext(ctx, 12*time.Second)
	defer cancel()
	query = strings.TrimSpace(query)
	if len([]byte(query)) < 2 || len([]byte(query)) > 200 {
		return nil, errors.New("search query must be between 2 and 200 bytes")
	}
	u, _ := url.Parse("https://skills.sh/api/search")
	q := u.Query()
	q.Set("q", query)
	q.Set("limit", "20")
	u.RawQuery = q.Encode()
	var response struct {
		Skills []struct {
			ID       string `json:"id"`
			Name     string `json:"name"`
			Installs int    `json:"installs"`
			Source   string `json:"source"`
		} `json:"skills"`
	}
	if err := m.getJSON(ctx, m.client(), u.String(), 256<<10, &response); err != nil {
		return nil, fmt.Errorf("search skills.sh: %w", err)
	}
	if len(response.Skills) > 20 {
		return nil, errors.New("skills.sh returned more than 20 search results")
	}
	out := make([]SearchResult, 0, len(response.Skills))
	for _, item := range response.Skills {
		parts := strings.Split(item.ID, "/")
		if len(parts) != 3 || !validName(parts[0]) || !validName(parts[1]) || !validName(parts[2]) || len(item.Name) == 0 || len(item.Name) > 120 || len(item.Source) > 120 || item.Source != parts[0]+"/"+parts[1] || item.Installs < 0 {
			continue
		}
		out = append(out, SearchResult{Name: item.Name, ID: item.ID, Source: item.Source, Installs: item.Installs, URL: "https://skills.sh/" + item.ID})
	}
	return out, nil
}

// Install copies one previously discovered skill. Existing destinations are
// never replaced. File modes are normalized and symlink entries are never
// requested because installation reads only GitHub blob paths.
func (m Manager) Install(ctx context.Context, source, skillPath string) (Installed, error) {
	var cancel context.CancelFunc
	ctx, cancel = boundedContext(ctx, 60*time.Second)
	defer cancel()
	candidates, err := m.Discover(ctx, source)
	if err != nil {
		return Installed{}, err
	}
	var candidate *Candidate
	for i := range candidates {
		if candidates[i].Path == skillPath {
			candidate = &candidates[i]
			break
		}
	}
	if candidate == nil {
		return Installed{}, errors.New("selected skill path is not in the discovered source")
	}
	if !validName(candidate.Name) {
		return Installed{}, errors.New("skill name is not a safe directory name")
	}
	root, err := m.ensureRoot()
	if err != nil {
		return Installed{}, err
	}
	dest := filepath.Join(root, candidate.Name)
	if _, err := os.Lstat(dest); err == nil {
		return Installed{}, fmt.Errorf("skill %q already exists in the managed directory", candidate.Name)
	} else if !errors.Is(err, os.ErrNotExist) {
		return Installed{}, fmt.Errorf("check skill destination: %w", err)
	}
	// Re-fetch the bounded tree and files; installation never trusts candidate paths
	// supplied by the caller without checking that the path remains in the source.
	s, err := parseSource(source)
	if err != nil {
		return Installed{}, err
	}
	client := m.client()
	var repo githubRepo
	if err := m.getJSON(ctx, client, "https://api.github.com/repos/"+s.owner+"/"+s.repo, maxTreeResponse, &repo); err != nil {
		return Installed{}, err
	}
	ref := s.ref
	if ref == "" {
		ref = repo.DefaultBranch
	}
	var tree githubTree
	endpoint := "https://api.github.com/repos/" + s.owner + "/" + s.repo + "/git/trees/" + url.PathEscape(ref) + "?recursive=1"
	if err := m.getJSON(ctx, client, endpoint, maxTreeResponse, &tree); err != nil {
		return Installed{}, err
	}
	if tree.Truncated {
		return Installed{}, errors.New("repository tree is too large to install safely")
	}
	prefix := strings.TrimSuffix(skillPath, "/") + "/"
	paths := make([]string, 0)
	for _, entry := range tree.Tree {
		if entry.Type != "blob" || entry.Mode == "120000" {
			continue
		}
		if skillPath == "." || strings.HasPrefix(entry.Path, prefix) {
			rel := entry.Path
			if skillPath != "." {
				rel = strings.TrimPrefix(entry.Path, prefix)
			}
			if safeRelative(rel) {
				paths = append(paths, rel)
			}
		}
	}
	if len(paths) == 0 || len(paths) > maxFiles {
		return Installed{}, fmt.Errorf("skill file count is outside the supported limit (1-%d)", maxFiles)
	}
	sort.Strings(paths)
	installed := Installed{Name: candidate.Name, Description: candidate.Description, Source: candidate.Source, Path: dest, InstalledAt: time.Now().UTC()}
	tmp, err := temporaryDirectory(root)
	if err != nil {
		return Installed{}, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = os.RemoveAll(tmp)
		}
	}()
	total := 0
	for _, rel := range paths {
		if err := ctx.Err(); err != nil {
			return Installed{}, err
		}
		data, err := m.getBytes(ctx, client, rawURL(s, ref, path.Join(skillPath, rel)), maxFileBytes)
		if err != nil {
			return Installed{}, fmt.Errorf("download %s: %w", rel, err)
		}
		total += len(data)
		if total > maxInstallBytes {
			return Installed{}, fmt.Errorf("skill contents exceed the %d-byte installation limit", maxInstallBytes)
		}
		if err := writeSkillFile(tmp, rel, data); err != nil {
			return Installed{}, err
		}
	}
	if _, err := os.Stat(filepath.Join(tmp, "SKILL.md")); err != nil {
		return Installed{}, errors.New("selected skill has no top-level SKILL.md")
	}
	meta := marker{Installed: installed, Files: paths}
	data, _ := json.MarshalIndent(meta, "", "  ")
	if err := writeSkillFile(tmp, ".pk-install.json", append(data, '\n')); err != nil {
		return Installed{}, err
	}
	if err := renameNoReplace(tmp, dest); err != nil {
		return Installed{}, fmt.Errorf("install skill: %w", err)
	}
	committed = true
	return installed, nil
}

func (m Manager) List() ([]Installed, error) {
	root, err := m.rootPath()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return []Installed{}, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Installed
	for _, e := range entries {
		if !e.IsDir() || !validName(e.Name()) {
			continue
		}
		b, err := os.ReadFile(filepath.Join(root, e.Name(), ".pk-install.json"))
		if err != nil {
			continue
		}
		var meta marker
		if json.Unmarshal(b, &meta) == nil && meta.Installed.Name == e.Name() {
			out = append(out, meta.Installed)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (m Manager) Remove(name string) error {
	if !validName(name) {
		return errors.New("invalid managed skill name")
	}
	root, err := m.rootPath()
	if err != nil {
		return err
	}
	dest := filepath.Join(root, name)
	info, err := os.Lstat(dest)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return errors.New("managed skill path is not a directory")
	}
	b, err := os.ReadFile(filepath.Join(dest, ".pk-install.json"))
	if err != nil {
		return errors.New("refusing to remove skill without pk installation provenance")
	}
	var meta marker
	if err := json.Unmarshal(b, &meta); err != nil || meta.Installed.Name != name {
		return errors.New("refusing to remove skill with invalid installation provenance")
	}
	return os.RemoveAll(dest)
}

func parseSource(raw string) (sourceRef, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return sourceRef{}, errors.New("skill source is required")
	}
	if !strings.Contains(raw, "://") {
		source, wanted, _ := strings.Cut(raw, "@")
		parts := strings.Split(source, "/")
		if len(parts) != 2 {
			return sourceRef{}, errors.New("source must be owner/repo[@skill] or an HTTPS GitHub/skills.sh URL")
		}
		return validateSource(sourceRef{owner: parts[0], repo: strings.TrimSuffix(parts[1], ".git"), wanted: wanted})
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil {
		return sourceRef{}, errors.New("skill source URL must use HTTPS")
	}
	switch strings.ToLower(u.Host) {
	case "github.com":
		p := strings.Split(strings.Trim(u.Path, "/"), "/")
		if len(p) < 2 {
			return sourceRef{}, errors.New("GitHub source URL must include owner and repository")
		}
		s := sourceRef{owner: p[0], repo: strings.TrimSuffix(p[1], ".git")}
		if len(p) >= 4 && p[2] == "tree" {
			s.ref = p[3]
			if len(p) > 4 {
				s.wanted = path.Base(path.Join(p[4:]...))
			}
		}
		return validateSource(s)
	case "skills.sh", "www.skills.sh":
		p := strings.Split(strings.Trim(u.Path, "/"), "/")
		if len(p) < 3 || p[0] == "p" {
			return sourceRef{}, errors.New("skills.sh URL must identify a GitHub skill page: https://skills.sh/<owner>/<repo>/<skill>")
		}
		return validateSource(sourceRef{owner: p[0], repo: p[1], wanted: p[len(p)-1]})
	default:
		return sourceRef{}, errors.New("only public GitHub repositories and skills.sh GitHub skill pages are supported")
	}
}
func validateSource(s sourceRef) (sourceRef, error) {
	if !validName(s.owner) || !validName(s.repo) {
		return sourceRef{}, errors.New("invalid GitHub owner/repository")
	}
	if s.ref != "" && (strings.Contains(s.ref, "..") || strings.ContainsAny(s.ref, "\\\x00")) {
		return sourceRef{}, errors.New("invalid Git ref")
	}
	return s, nil
}
func (m Manager) getJSON(ctx context.Context, c *http.Client, endpoint string, limit int64, out any) error {
	b, err := m.getBytes(ctx, c, endpoint, limit)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, out); err != nil {
		return fmt.Errorf("decode remote JSON response: %w", err)
	}
	return nil
}
func (m Manager) getBytes(ctx context.Context, c *http.Client, endpoint string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "pk-skill-manager")
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		if resp.StatusCode == http.StatusForbidden && resp.Header.Get("X-RateLimit-Remaining") == "0" {
			return nil, errors.New("GitHub API rate limit reached; wait for the limit to reset, then retry skill browsing")
		}
		if resp.StatusCode == http.StatusNotFound {
			return nil, errors.New("GitHub repository or selected skill file was not found; check the source URL and ref")
		}
		return nil, fmt.Errorf("remote returned HTTP %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("remote response exceeds %d-byte limit", limit)
	}
	return b, nil
}
func rawURL(s sourceRef, ref, p string) string {
	return "https://raw.githubusercontent.com/" + s.owner + "/" + s.repo + "/" + escapePath(ref) + "/" + escapePath(p)
}

func escapePath(p string) string {
	parts := strings.Split(p, "/")
	for i := range parts {
		parts[i] = url.PathEscape(parts[i])
	}
	return strings.Join(parts, "/")
}
func parseSkill(b []byte) (string, string, error) {
	text := string(b)
	if !strings.HasPrefix(text, "---\n") {
		return "", "", errors.New("missing YAML frontmatter")
	}
	end := strings.Index(text[4:], "\n---")
	if end < 0 {
		return "", "", errors.New("unterminated frontmatter")
	}
	var name, desc string
	for _, line := range strings.Split(text[4:4+end], "\n") {
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		v = strings.Trim(strings.TrimSpace(v), "\"'")
		switch strings.TrimSpace(k) {
		case "name":
			name = v
		case "description":
			desc = v
		}
	}
	if !validName(name) || strings.TrimSpace(desc) == "" {
		return "", "", errors.New("frontmatter requires safe name and description")
	}
	if len(desc) > 512 {
		desc = desc[:512]
	}
	return name, desc, nil
}

func boundedContext(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithTimeout(ctx, timeout)
}
func safeRelative(p string) bool {
	if p == "" || strings.ContainsAny(p, "\\:\x00") || strings.HasPrefix(p, "/") || filepath.IsAbs(filepath.FromSlash(p)) || filepath.VolumeName(filepath.FromSlash(p)) != "" {
		return false
	}
	clean := path.Clean(p)
	return clean == p && clean != "." && clean != ".." && !strings.HasPrefix(clean, "../")
}

func validName(name string) bool {
	if !safeName.MatchString(name) || name == "." || name == ".." || strings.HasSuffix(name, ".") {
		return false
	}
	switch strings.ToUpper(name) {
	case "CON", "PRN", "AUX", "NUL", "COM1", "COM2", "COM3", "COM4", "COM5", "COM6", "COM7", "COM8", "COM9", "LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9":
		return false
	}
	return true
}
func temporaryDirectory(root string) (string, error) {
	var random [8]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", err
	}
	name := ".install-" + hex.EncodeToString(random[:])
	p := filepath.Join(root, name)
	if err := os.Mkdir(p, 0o700); err != nil {
		return "", err
	}
	return p, nil
}

func (m Manager) rootPath() (string, error) {
	if strings.TrimSpace(m.Root) == "" {
		return "", errors.New("managed skills root is empty")
	}
	p, err := filepath.Abs(m.Root)
	if err != nil {
		return "", err
	}
	p = filepath.Clean(p)
	info, statErr := os.Lstat(p)
	if statErr == nil && (!info.IsDir() || info.Mode()&os.ModeSymlink != 0) {
		return "", errors.New("managed skills root must be a real directory")
	}
	if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return "", statErr
	}
	return p, nil
}
func (m Manager) ensureRoot() (string, error) {
	p, err := m.rootPath()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(p, 0o700); err != nil {
		return "", err
	}
	info, err := os.Lstat(p)
	if err != nil {
		return "", err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("managed skills root must be a real directory")
	}
	if err := os.Chmod(p, 0o700); err != nil {
		return "", err
	}
	return p, nil
}
func writeSkillFile(root, rel string, data []byte) error {
	if !safeRelative(rel) {
		return fmt.Errorf("unsafe skill file path %q", rel)
	}
	target := filepath.Join(root, filepath.FromSlash(rel))
	dir := filepath.Dir(target)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return os.WriteFile(target, data, 0o600)
}
func renameNoReplace(old, new string) error {
	if _, err := os.Lstat(new); err == nil {
		return os.ErrExist
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return os.Rename(old, new)
}
