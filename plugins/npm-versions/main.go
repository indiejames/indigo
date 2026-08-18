// Command npm-versions is an example CompletionProvider plugin: it offers
// published version numbers as completions while editing the "dependencies"/
// "devDependencies"/etc. blocks of a package.json file, similar to VS Code's
// built-in npm IntelliSense. It talks directly to the public npm registry —
// nothing npm- or Node-specific runs inside the editor itself, so users who
// don't have npm installed (or don't want this plugin) are unaffected; it's
// purely opt-in like any other plugin.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/indiejames/indigo/sdk"
)

// cacheTTL controls how long a package's version list is trusted before a
// background refresh is kicked off again.
const cacheTTL = 10 * time.Minute

// inflightWait bounds how long getCompletions will wait for an already-in-
// -flight registry fetch before giving up and returning whatever's cached
// (possibly nothing). It's comfortably under Manager.GetCompletions's own
// 500ms per-provider timeout (internal/plugin/manager.go), leaving margin
// for RPC/goroutine-scheduling overhead — without this wait, the very first
// completion request for any package would always return empty, since the
// fetch it kicks off can't possibly land before the request itself returns.
// A var (not const) so tests can shrink it instead of waiting out the real
// duration.
var inflightWait = 400 * time.Millisecond

// fetchVersions is a seam over the real npm-registry HTTP call, so tests can
// swap in a fake without a network round trip — the same pattern indigo's own
// clipboard package uses for pbcopy/pbpaste.
var fetchVersions = fetchNpmVersions

type npmVersionsPlugin struct {
	api *sdk.Api

	mu       sync.Mutex
	cache    map[string]cacheEntry
	inflight map[string]chan struct{} // dep -> closed when its background fetch finishes
}

type cacheEntry struct {
	versions []versionInfo
	fetched  time.Time
}

type versionInfo struct {
	version string
	latest  bool
}

func (p *npmVersionsPlugin) Init(api *sdk.Api) sdk.Info {
	p.api = api
	p.cache = make(map[string]cacheEntry)
	p.inflight = make(map[string]chan struct{})
	api.Completions(p.getCompletions) //nolint:errcheck
	return sdk.Info{Name: "npm-versions", Version: "0.1.0"}
}

// getCompletions is called on every completion request, so it must return
// quickly: it serves from the in-memory cache, waiting up to inflightWait for
// an in-flight registry fetch when the cache is cold or stale rather than
// always returning nothing on the first request for a package (see
// versionsFor).
func (p *npmVersionsPlugin) getCompletions(bufID, line, col uint32) []sdk.CompletionItem {
	path, _, _, _, err := p.api.BufferInfo(bufID)
	if err != nil || filepath.Base(path) != "package.json" {
		return nil
	}
	content, err := p.api.ReadBuffer(bufID)
	if err != nil {
		return nil
	}
	dep, valStart, valEnd, ok := depValueAt(content, int(line), int(col))
	if !ok {
		return nil
	}

	versions := p.versionsFor(dep)
	if len(versions) == 0 {
		return nil
	}

	items := make([]sdk.CompletionItem, len(versions))
	for i, v := range versions {
		detail := ""
		if v.latest {
			detail = "latest"
		}
		items[i] = sdk.CompletionItem{
			Label:      v.version,
			Detail:     detail,
			InsertText: v.version,
			// Preserve registry (newest-first) order instead of the
			// client's default alphabetical sort, which would put "10.0.0"
			// before "9.0.0".
			SortText: fmt.Sprintf("%05d", i),
			TextEdit: &sdk.TextEdit{
				From:    sdk.Position{Line: line, Col: uint32(valStart)},
				To:      sdk.Position{Line: line, Col: uint32(valEnd)},
				NewText: v.version,
			},
		}
	}
	return items
}

// versionsFor returns dep's cached versions, refreshing them in the
// background when the cache is missing or stale. Rather than always
// returning immediately (which would mean the very first request for any
// package — the common case — always comes back empty, since a fresh
// background fetch can't possibly land before the request that kicked it off
// returns), it waits up to inflightWait for a fetch already in flight: a
// typical registry response lands well within that window, so most first
// requests already have real data by the time this returns. A slower
// response just means this particular request still comes back empty — the
// fetch keeps running, and the next request (the user's next keystroke)
// finds a warm cache.
func (p *npmVersionsPlugin) versionsFor(dep string) []versionInfo {
	p.mu.Lock()
	entry, ok := p.cache[dep]
	stale := !ok || time.Since(entry.fetched) > cacheTTL
	var wait chan struct{}
	if stale {
		if ch, inflight := p.inflight[dep]; inflight {
			wait = ch
		} else {
			ch = make(chan struct{})
			p.inflight[dep] = ch
			wait = ch
			go p.refresh(dep, ch)
		}
	}
	p.mu.Unlock()

	if wait == nil {
		return entry.versions
	}
	select {
	case <-wait:
	case <-time.After(inflightWait):
	}

	p.mu.Lock()
	entry = p.cache[dep]
	p.mu.Unlock()
	return entry.versions
}

// refresh fetches dep's versions and updates the cache, then closes done to
// wake any versionsFor call waiting on this particular fetch.
func (p *npmVersionsPlugin) refresh(dep string, done chan struct{}) {
	defer func() {
		p.mu.Lock()
		if p.inflight[dep] == done {
			delete(p.inflight, dep)
		}
		p.mu.Unlock()
		close(done)
	}()

	versions, err := fetchVersions(dep)
	if err != nil {
		// Leave any previously-cached versions in place rather than
		// clearing them on a transient failure (e.g. offline) — stale
		// suggestions are more useful than none. See TrimHistory/lint
		// Manager for the same "don't discard on error" philosophy
		// elsewhere in this codebase.
		return
	}
	p.mu.Lock()
	p.cache[dep] = cacheEntry{versions: versions, fetched: time.Now()}
	p.mu.Unlock()
}

// depValueAt reports whether (line, col) sits inside a dependency version
// string in content — e.g. the "^4.17.21" in `"lodash": "^4.17.21"` nested
// under a "dependencies" object — returning the dependency's package name and
// the [valStart, valEnd) column range of the value text (excluding quotes)
// that a completion should replace, for use as its TextEdit range.
//
// If the value already starts with a semver range operator ('^' or '~'),
// valStart is advanced past it: a completion only ever supplies a bare
// version number, so replacing the operator too would silently drop it
// (e.g. accepting "4.17.21" over "^4.1" would leave "4.17.21" instead of the
// intended "^4.17.21"). The operator, if present, is left untouched and the
// version is inserted right after it.
//
// npm also supports richer range syntax this plugin doesn't understand:
// comparator sets (">=1.2.3 <2.0.0"), OR sets ("1.2.3 || 2.0.0"), and
// X-ranges ("1.2.x"). Correctly completing "the version token under the
// cursor" within one of those would mean actually parsing the range — rather
// than risk corrupting a range it doesn't understand (replacing the whole
// thing with a bare version, destroying the other comparators), depValueAt
// declines to match at all when the value isn't one of the simple forms
// above (see isSimpleVersionValue).
//
// This is a line-oriented heuristic tuned for package.json's near-universal
// one-key-per-line pretty-printed style, not a general JSON parser: it won't
// find matches in a minified/single-line package.json. That's an acceptable
// degradation (completions just don't fire) rather than a correctness bug.
func depValueAt(content string, line, col int) (dep string, valStart, valEnd int, ok bool) {
	lines := strings.Split(content, "\n")
	if line < 0 || line >= len(lines) {
		return "", 0, 0, false
	}
	if dependencySectionAt(lines, line) == "" {
		return "", 0, 0, false
	}
	raw := lines[line]
	idx := depValueLineRe.FindStringSubmatchIndex(raw)
	if idx == nil {
		return "", 0, 0, false
	}
	keyStart, keyEnd := idx[2], idx[3]
	valStart, valEnd = idx[4], idx[5]
	if !isSimpleVersionValue(raw[valStart:valEnd]) {
		return "", 0, 0, false
	}
	if valStart < valEnd && (raw[valStart] == '^' || raw[valStart] == '~') {
		valStart++
	}
	if col < valStart || col > valEnd {
		return "", 0, 0, false
	}
	return raw[keyStart:keyEnd], valStart, valEnd, true
}

// complexRangeCharsRe matches characters that only show up in npm range
// syntax this plugin doesn't attempt to parse: whitespace and '||'
// (comparator/OR sets), '<'/'>'/'=' (comparators), and 'x'/'X'/'*'
// (X-ranges/wildcards). Deliberately excludes '-': a bare hyphen is also used
// in ordinary prerelease tags ("1.2.3-beta.1"), and npm's hyphen ranges
// ("1.2.3 - 2.3.4") always include surrounding whitespace this already
// catches, so excluding it avoids rejecting valid plain versions.
var complexRangeCharsRe = regexp.MustCompile(`[\s|<>=xX*]`)

// isSimpleVersionValue reports whether value is a form this plugin knows how
// to safely replace: empty, a bare version, or a single leading '^'/'~' plus
// a bare version.
func isSimpleVersionValue(value string) bool {
	v := strings.TrimPrefix(strings.TrimPrefix(value, "^"), "~")
	return !complexRangeCharsRe.MatchString(v)
}

var (
	// depValueLineRe matches a single `"key": "value"` line, capturing key
	// and value separately (column offsets come from the capture indices).
	depValueLineRe = regexp.MustCompile(`^\s*"([^"]+)"\s*:\s*"([^"]*)"\s*,?\s*$`)
	// sectionOpenRe matches a line opening one of the dependency-map objects.
	sectionOpenRe = regexp.MustCompile(`^"(dependencies|devDependencies|peerDependencies|optionalDependencies|bundledDependencies|bundleDependencies)"\s*:\s*\{$`)
)

// dependencySectionAt walks upward from targetLine tracking brace depth,
// returning the name of the dependency-map object targetLine is nested
// directly inside, or "" if it isn't inside one (e.g. it's inside "scripts",
// or at the top level).
func dependencySectionAt(lines []string, targetLine int) string {
	depth := 0
	for i := targetLine - 1; i >= 0; i-- {
		trimmed := strings.TrimSpace(lines[i])
		switch {
		case trimmed == "}" || trimmed == "},":
			depth++
		case depth == 0 && sectionOpenRe.MatchString(trimmed):
			return sectionOpenRe.FindStringSubmatch(trimmed)[1]
		case strings.HasSuffix(trimmed, "{"):
			if depth == 0 {
				// Inside some other (non-dependency) nested object.
				return ""
			}
			depth--
		}
	}
	return ""
}

// maxVersions caps how many completion items a single response carries —
// old packages can have hundreds of published versions, and there's no
// value in shipping all of them over the wire on every keystroke.
const maxVersions = 50

// fetchNpmVersions queries the public npm registry for dep's published
// versions, newest first (by publish time), with the "latest" dist-tag
// flagged.
func fetchNpmVersions(dep string) ([]versionInfo, error) {
	req, err := http.NewRequest(http.MethodGet, "https://registry.npmjs.org/"+dep, nil)
	if err != nil {
		return nil, err
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("registry.npmjs.org returned %s for %q", resp.Status, dep)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
	if err != nil {
		return nil, err
	}
	return parseRegistryResponse(body)
}

// parseRegistryResponse decodes the npm registry's package metadata JSON
// into a newest-first version list. Split out from fetchNpmVersions so tests
// can exercise the parsing/sorting logic directly against fixture JSON,
// without a network round trip.
func parseRegistryResponse(body []byte) ([]versionInfo, error) {
	var payload struct {
		Versions map[string]json.RawMessage `json:"versions"`
		Time     map[string]string          `json:"time"`
		DistTags map[string]string          `json:"dist-tags"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	latest := payload.DistTags["latest"]

	versions := make([]string, 0, len(payload.Versions))
	for v := range payload.Versions {
		versions = append(versions, v)
	}
	// ISO-8601 timestamps sort lexicographically in chronological order;
	// versions missing from Time (rare) sort last via the empty-string
	// fallback.
	sort.Slice(versions, func(i, j int) bool {
		return payload.Time[versions[i]] > payload.Time[versions[j]]
	})
	if len(versions) > maxVersions {
		versions = versions[:maxVersions]
	}

	out := make([]versionInfo, len(versions))
	for i, v := range versions {
		out[i] = versionInfo{version: v, latest: v == latest}
	}
	return out, nil
}

func main() {
	if err := sdk.Run(&npmVersionsPlugin{}); err != nil {
		panic(err)
	}
}
