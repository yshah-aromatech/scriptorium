// Package scripts ports src/Scripts.psm1: multi-repo sync (clone / hard
// reset), the legacy-layout migration, and script discovery/detail.
package scripts

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/yshah-aromatech/scriptorium/internal/config"
)

// Repo is one normalized scripts repo — a clone target under ScriptsDir
// (or, for the legacy single-repo layout, ScriptsDir itself).
type Repo struct {
	Name, URL, Branch, Root string
	Legacy                  bool
}

// repoNamePattern is the valid repos-entry name shape (src/Core.psm1's
// '^[A-Za-z0-9_-]+$'), shared by Repos' normalization and AddRepoConfig's
// name validation.
var repoNamePattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// invalidNameChar matches every character AddRepoConfig's derived-name
// sanitizer replaces with '-' (src/Core.psm1: `-replace '[^A-Za-z0-9_-]', '-'`).
var invalidNameChar = regexp.MustCompile(`[^A-Za-z0-9_-]`)

// basenameNoExt ports `[IO.Path]::GetFileNameWithoutExtension(($url -replace
// '/+$', ”))`: trailing slashes stripped, then the last path segment with
// its final extension (the text after the last '.', when that '.' isn't the
// segment's first character) removed. URLs always use '/', so this uses the
// "path" package rather than the platform-dependent "path/filepath".
func basenameNoExt(url string) string {
	base := path.Base(strings.TrimRight(url, "/"))
	if idx := strings.LastIndex(base, "."); idx > 0 {
		base = base[:idx]
	}
	return base
}

// sanitizeRepoName replaces every character outside [A-Za-z0-9_-] with '-'.
func sanitizeRepoName(s string) string {
	return invalidNameChar.ReplaceAllString(s, "-")
}

// normalizeRepoURL strips embedded credentials, a trailing .git, and
// trailing slashes, so two spellings of the same remote compare equal
// (src/Core.psm1's `$normalize` scriptblock, shared by AddRepoConfig's
// duplicate check and Update-StoRepoLayout's remote match).
func normalizeRepoURL(u string) string {
	u = credentialsPattern.ReplaceAllString(u, "//")
	u = gitSuffixPattern.ReplaceAllString(u, "")
	u = trailingSlashPattern.ReplaceAllString(u, "")
	return u
}

var (
	credentialsPattern   = regexp.MustCompile(`//[^@/]+@`)
	gitSuffixPattern     = regexp.MustCompile(`\.git/?$`)
	trailingSlashPattern = regexp.MustCompile(`/+$`)
)

// Repos is the port of Get-StoRepos: normalizes cfg.Repos into clone
// targets, or — when no entries are configured — the single legacy repo
// (present even with an empty URL, since discovery still reads a
// hand-populated ScriptsDir).
func Repos(cfg *config.Config, paths config.Paths) []Repo {
	if len(cfg.Repos) > 0 {
		var repos []Repo
		for _, e := range cfg.Repos {
			if e.URL == "" {
				continue
			}
			name := e.Name
			if name == "" {
				name = basenameNoExt(e.URL)
			}
			if !repoNamePattern.MatchString(name) {
				continue
			}
			branch := e.Branch
			if branch == "" {
				branch = "main"
			}
			repos = append(repos, Repo{
				Name:   name,
				URL:    e.URL,
				Branch: branch,
				Root:   filepath.Join(paths.ScriptsDir, name),
				Legacy: false,
			})
		}
		return repos
	}

	url := os.Getenv("SCRIPTS_REPO")
	if url == "" {
		url = cfg.ScriptsRepo
	}
	return []Repo{{
		Name:   "scripts",
		URL:    url,
		Branch: cfg.Branch,
		Root:   paths.ScriptsDir,
		Legacy: true,
	}}
}

// repoFields is the JSON shape of one repos-array entry, in the field order
// PS's `[pscustomobject]@{ name=...; url=...; branch=... }` writes.
type repoFields struct {
	Name   string `json:"name"`
	URL    string `json:"url"`
	Branch string `json:"branch"`
}

// AddRepoConfig is the port of Add-StoRepoConfig: adds a repo to
// config.json's `repos` array (read-modify-write on the raw JSON object, so
// unrelated keys — and unrelated fields on other repo entries — survive
// untouched). A pre-existing legacy `scriptsRepo` is converted into its own
// entry first, so it keeps syncing after the config gains `repos`.
func AddRepoConfig(appDir, url, name, branch string) (ok bool, message, resolvedName string) {
	if name == "" {
		name = sanitizeRepoName(basenameNoExt(url))
	}
	if !repoNamePattern.MatchString(name) {
		return false, fmt.Sprintf("invalid repo name '%s' — use letters/digits/dash/underscore", name), name
	}
	if branch == "" {
		branch = "main"
	}

	cfgFile := filepath.Join(appDir, "config.json")
	var keys []string
	var raws []json.RawMessage
	if data, err := os.ReadFile(cfgFile); err == nil {
		k, r, perr := orderedJSONObject(data)
		if perr != nil {
			return false, fmt.Sprintf("config.json is not valid JSON: %v", perr), name
		}
		keys, raws = k, r
	} else if !os.IsNotExist(err) {
		return false, fmt.Sprintf("could not read config.json: %v", err), name
	}

	reposIdx := -1
	for i, k := range keys {
		if strings.EqualFold(k, "repos") {
			reposIdx = i
			break
		}
	}
	var existingReposRaw json.RawMessage
	if reposIdx >= 0 {
		existingReposRaw = raws[reposIdx]
	}

	type entry struct {
		fields repoFields
		raw    json.RawMessage // non-nil: carried over verbatim (preserves unknown fields)
	}
	var repos []entry
	for _, elem := range decodeReposRaw(existingReposRaw) {
		var f repoFields
		_ = json.Unmarshal(elem, &f)
		if f.URL == "" {
			continue
		}
		repos = append(repos, entry{fields: f, raw: elem})
	}

	scriptsRepo := rawStringByKey(keys, raws, "scriptsRepo")
	if len(repos) == 0 && scriptsRepo != "" {
		legacyName := sanitizeRepoName(basenameNoExt(scriptsRepo))
		if strings.EqualFold(legacyName, name) {
			legacyName += "-legacy"
		}
		legacyBranch := rawStringByKey(keys, raws, "branch")
		if legacyBranch == "" {
			legacyBranch = "main"
		}
		repos = append(repos, entry{fields: repoFields{Name: legacyName, URL: scriptsRepo, Branch: legacyBranch}})
	}

	for _, e := range repos {
		if strings.EqualFold(e.fields.Name, name) {
			return false, fmt.Sprintf("a repo named '%s' already exists — pass --name to pick another", name), name
		}
		if normalizeRepoURL(e.fields.URL) == normalizeRepoURL(url) {
			return false, fmt.Sprintf("repo already configured as '%s': %s", e.fields.Name, e.fields.URL), e.fields.Name
		}
	}

	repos = append(repos, entry{fields: repoFields{Name: name, URL: url, Branch: branch}})

	reposElems := make([]json.RawMessage, len(repos))
	for i, e := range repos {
		if e.raw != nil {
			reposElems[i] = e.raw
			continue
		}
		b, _ := json.Marshal(e.fields)
		reposElems[i] = b
	}
	newReposRaw, _ := json.Marshal(reposElems)

	if reposIdx >= 0 {
		raws[reposIdx] = newReposRaw
	} else {
		keys = append(keys, "repos")
		raws = append(raws, newReposRaw)
	}

	out := marshalOrderedObject(keys, raws)
	if err := os.WriteFile(cfgFile, out, 0o644); err != nil {
		return false, fmt.Sprintf("failed to write config.json: %v", err), name
	}

	return true, fmt.Sprintf("added repo '%s' (%s, branch %s) — %d repo(s) configured", name, url, branch, len(repos)), name
}

// decodeReposRaw parses a raw "repos" config value into its element
// messages, mirroring PS's `@($cfg.repos)` wrapping: a bare JSON object
// wraps to one element, and a literal null wraps to one null element (which
// the caller's url-emptiness filter then drops) — see config.decodeRepos
// for the same rule applied to the typed decode path.
func decodeReposRaw(raw json.RawMessage) []json.RawMessage {
	if raw == nil {
		return nil
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return []json.RawMessage{[]byte("null")}
	}
	var elems []json.RawMessage
	if json.Unmarshal(raw, &elems) != nil {
		var obj map[string]json.RawMessage
		if json.Unmarshal(raw, &obj) != nil {
			return nil
		}
		elems = []json.RawMessage{raw}
	}
	return elems
}

// rawStringByKey looks up key case-insensitively (PS hashtable/PSObject
// property lookup semantics) among a parsed top-level object's keys/raws,
// returning "" when absent or not a JSON string.
func rawStringByKey(keys []string, raws []json.RawMessage, key string) string {
	for i, k := range keys {
		if strings.EqualFold(k, key) {
			var s string
			if json.Unmarshal(raws[i], &s) == nil {
				return s
			}
			return ""
		}
	}
	return ""
}

// orderedJSONObject decodes a top-level JSON object's keys and raw values
// in document order (a leading UTF-8 BOM is stripped first, mirroring
// Get-Content's automatic BOM detection).
func orderedJSONObject(data []byte) (keys []string, raws []json.RawMessage, err error) {
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	dec := json.NewDecoder(bytes.NewReader(data))
	tok, err := dec.Token()
	if err != nil {
		return nil, nil, err
	}
	delim, ok := tok.(json.Delim)
	if !ok || delim != '{' {
		return nil, nil, fmt.Errorf("expected a top-level JSON object")
	}
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return nil, nil, err
		}
		key, ok := keyTok.(string)
		if !ok {
			return nil, nil, fmt.Errorf("expected a string key")
		}
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return nil, nil, err
		}
		keys = append(keys, key)
		raws = append(raws, raw)
	}
	return keys, raws, nil
}

// marshalOrderedObject renders keys/raws back into a JSON object, preserving
// their order — encoding/json has no ordered-map marshal, so this writes
// the object by hand.
func marshalOrderedObject(keys []string, raws []json.RawMessage) []byte {
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			buf.WriteByte(',')
		}
		buf.WriteByte('\n')
		buf.WriteString("  ")
		kb, _ := json.Marshal(k)
		buf.Write(kb)
		buf.WriteString(": ")
		buf.Write(raws[i])
	}
	buf.WriteString("\n}\n")
	return buf.Bytes()
}
