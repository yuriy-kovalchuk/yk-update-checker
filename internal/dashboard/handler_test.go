package dashboard

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/yuriy-kovalchuk/yk-update-checker/internal/errkind"
	"github.com/yuriy-kovalchuk/yk-update-checker/internal/report"
	"github.com/yuriy-kovalchuk/yk-update-checker/internal/scan"
	"github.com/yuriy-kovalchuk/yk-update-checker/internal/trigger"
)

type fakeTrigger struct {
	available, running bool
	err                error
}

func (f fakeTrigger) Trigger(context.Context) error { return f.err }
func (f fakeTrigger) Available() bool               { return f.available }
func (f fakeTrigger) Running(context.Context) bool  { return f.running }

func newTestMux(snap report.Snapshot, trig fakeTrigger) *http.ServeMux {
	mux := http.NewServeMux()
	(&Handler{Snapshot: func() report.Snapshot { return snap }, Trigger: trig}).RegisterRoutes(mux)
	return mux
}

func get(mux *http.ServeMux, method, path string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequestWithContext(context.Background(), method, path, nil))
	return w
}

func TestUIServesEmbeddedHTML(t *testing.T) {
	mux := newTestMux(report.Snapshot{}, fakeTrigger{})

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type = %q, want text/html prefix", ct)
	}
	if !strings.Contains(w.Body.String(), "<title>Update Checker</title>") {
		t.Error("served body is not the embedded UI (missing expected title)")
	}
}

func TestFaviconServesSVG(t *testing.T) {
	mux := newTestMux(report.Snapshot{}, fakeTrigger{})

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/favicon.svg", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
	if ct := w.Header().Get("Content-Type"); ct != "image/svg+xml" {
		t.Errorf("Content-Type = %q, want image/svg+xml", ct)
	}
	if !strings.Contains(w.Body.String(), "<svg") {
		t.Error("served body is not SVG")
	}
}

func TestStaticAssetsServed(t *testing.T) {
	// The page links these by path; if the embed or a route breaks, the UI is a blank page.
	cases := []struct {
		path        string
		contentType string
	}{
		{"/assets/app.css", "text/css"},
		{"/assets/app.js", "text/javascript"},
		{"/assets/theme.js", "text/javascript"},
		{"/fonts/roboto-latin-var.woff2", "font/woff2"},
		{"/fonts/roboto-mono-latin-var.woff2", "font/woff2"},
	}
	mux := newTestMux(report.Snapshot{}, fakeTrigger{})

	for _, tc := range cases {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequestWithContext(context.Background(), http.MethodGet, tc.path, nil))

		if w.Code != http.StatusOK {
			t.Errorf("%s status = %d, want %d", tc.path, w.Code, http.StatusOK)
			continue
		}
		if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, tc.contentType) {
			t.Errorf("%s Content-Type = %q, want %q prefix", tc.path, ct, tc.contentType)
			continue
		}
		if w.Body.Len() == 0 {
			t.Errorf("%s body is empty", tc.path)
			continue
		}
		if cc := w.Header().Get("Cache-Control"); cc == "" {
			t.Errorf("%s has no Cache-Control", tc.path)
			continue
		}

		if tc.path == "/assets/app.css" {
			ui := httptest.NewRecorder()
			mux.ServeHTTP(ui, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil))
			if !strings.Contains(ui.Body.String(), `href="/assets/app.css"`) {
				t.Error("embedded UI does not link /assets/app.css")
			}
		}
	}
}

func TestResultsReturnsJSON(t *testing.T) {
	snap := report.Snapshot{Deps: []report.Dependency{{Status: report.DepStatus{Result: scan.Result{
		Source: "homelab", Dependency: "podinfo", CurrentVersion: "1.0.0", LatestVersion: "2.0.0", UpdateAvailable: true}}}}}
	w := get(newTestMux(snap, fakeTrigger{}), http.MethodGet, "/api/results")
	var got []scan.Result
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if w.Code != http.StatusOK || len(got) != 1 || got[0].Dependency != "podinfo" || !got[0].UpdateAvailable {
		t.Errorf("status %d, results %+v; want the stored dependency", w.Code, got)
	}
}

func TestResultsEmptyIsJSONArray(t *testing.T) {
	// The UI relies on [] (not null) for an empty result set.
	if body := strings.TrimSpace(get(newTestMux(report.Snapshot{}, fakeTrigger{}), http.MethodGet, "/api/results").Body.String()); body != "[]" {
		t.Errorf("body = %q, want []", body)
	}
}

func TestStatusSummarizesRepositories(t *testing.T) {
	older, newer := metav1.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), metav1.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	snap := report.Snapshot{
		Repos: []report.RepositoryScan{
			{Status: report.RepoStatus{Repo: "ok", LastScanTime: older, Phase: report.PhaseSucceeded}},
			{Status: report.RepoStatus{Repo: "bad", LastScanTime: newer, Phase: report.PhaseFailed,
				Error: &report.ErrorInfo{Kind: errkind.Auth, Message: "denied"}}},
		},
		Deps: make([]report.Dependency, 3),
	}
	var got Status
	if err := json.Unmarshal(get(newTestMux(snap, fakeTrigger{available: true, running: true}), http.MethodGet, "/api/status").Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !got.Scanning || !got.TriggerAvailable || got.ResultCount != 3 || got.LastScanAt == nil || !got.LastScanAt.Equal(newer.Time) {
		t.Errorf("status = %+v, want scanning, 3 results, last scan %v", got, newer)
	}
	if got.LastErrorKind != errkind.Auth || len(got.RepoErrors) != 1 || got.RepoErrors[0].Repo != "bad" {
		t.Errorf("errors = %q %+v, want bad/auth", got.LastErrorKind, got.RepoErrors)
	}
}

// TestAPIResponsesAreNotCacheable: the UI's polls must not be answered from an ingress or sidecar cache.
func TestAPIResponsesAreNotCacheable(t *testing.T) {
	for _, path := range []string{"/api/results", "/api/status"} {
		if cc := get(newTestMux(report.Snapshot{}, fakeTrigger{}), http.MethodGet, path).Header().Get("Cache-Control"); cc != "no-store" {
			t.Errorf("%s Cache-Control = %q, want no-store", path, cc)
		}
	}
}

// TestDashboardFooterUsesTheScanTime: a stored dependency's checked_at only moves when that reference changes, so it cannot say when the scan ran.
func TestDashboardFooterUsesTheScanTime(t *testing.T) {
	js, err := fs.ReadFile(uiFS, "ui/app.js")
	if err != nil {
		t.Fatal(err)
	}
	start := bytes.Index(js, []byte("function renderFooter()"))
	if start < 0 {
		t.Fatal("renderFooter not found in app.js")
	}
	body := js[start:]
	end := bytes.Index(body, []byte("\n}"))
	if end < 0 {
		t.Fatal("renderFooter is not a function")
	}
	body = body[:end]
	if !bytes.Contains(body, []byte("last_scan_at")) {
		t.Error("renderFooter does not use status.last_scan_at")
	}
	if bytes.Contains(body, []byte("checked_at")) {
		t.Error("renderFooter still infers the scan time from a dependency's checked_at")
	}
}

func TestTriggerResponses(t *testing.T) {
	cases := []struct {
		trig fakeTrigger
		want int
	}{
		{fakeTrigger{available: true}, http.StatusAccepted},
		{fakeTrigger{available: true, err: trigger.ErrAlreadyRunning}, http.StatusConflict},
		{fakeTrigger{}, http.StatusServiceUnavailable},
		{fakeTrigger{available: true, err: errors.New("boom")}, http.StatusInternalServerError},
	}
	for _, tc := range cases {
		if got := get(newTestMux(report.Snapshot{}, tc.trig), http.MethodPost, "/api/scan/trigger").Code; got != tc.want {
			t.Errorf("%+v: status %d, want %d", tc.trig, got, tc.want)
		}
	}
}

// TestThemeScriptRunsBeforeTheStylesheet: without it a dark preference paints white first, because
// theme.js sets data-theme synchronously in <head> and app.js is deferred.
func TestThemeScriptRunsBeforeTheStylesheet(t *testing.T) {
	html := readUI(t, "ui/index.html")
	script := strings.Index(html, `src="/assets/theme.js"`)
	style := strings.Index(html, `href="/assets/app.css"`)
	if script < 0 || style < 0 {
		t.Fatal("index.html links neither /assets/theme.js nor /assets/app.css")
	}
	if script > style {
		t.Error("theme.js is linked after the stylesheet, so the first paint can use the wrong palette")
	}
	tag := html[strings.LastIndex(html[:script], "<script"):script]
	if strings.Contains(tag, "defer") || strings.Contains(tag, "async") {
		t.Error("theme.js must not be deferred or async: it sets data-theme before the first paint")
	}
}

// TestNoInlineScriptOrHandlers: the CSP is script-src 'self', so an inline handler is dropped
// silently and the control just stops working.
func TestNoInlineScriptOrHandlers(t *testing.T) {
	for _, name := range []string{"ui/index.html", "ui/app.js", "ui/app.css"} {
		body := readUI(t, name)
		for _, tag := range regexp.MustCompile(`<script[^>]*>`).FindAllString(body, -1) {
			if !strings.Contains(tag, "src=") {
				t.Errorf("%s has a <script> without src: %s", name, tag)
			}
		}
		if regexp.MustCompile(`\son[a-z]+=`).MatchString(body) {
			t.Errorf("%s has an inline event handler", name)
		}
	}
}

// TestDashboardIconsResolve keeps every glyph app.js and index.html ask for in app.js's ICONS table,
// and keeps the table free of paths nothing draws. An unknown name renders no icon at all, and the
// theme toggle is the only place a glyph lives outside the table.
func TestDashboardIconsResolve(t *testing.T) {
	js := readUI(t, "ui/app.js")
	html := readUI(t, "ui/index.html")

	start := strings.Index(js, "const ICONS = {")
	if start < 0 {
		t.Fatal("ICONS table not found in app.js")
	}
	end := strings.Index(js[start:], "\n};")
	if end < 0 {
		t.Fatal("ICONS table is not closed")
	}
	table, rest := js[start:start+end], js[start+end:]

	defined := map[string]bool{}
	for _, line := range strings.Split(table, "\n") {
		key, _, found := strings.Cut(strings.TrimSpace(line), ":")
		if found && regexp.MustCompile(`^[a-z_]+$`).MatchString(key) {
			defined[key] = true
		}
	}
	if len(defined) < 10 {
		t.Fatalf("ICONS table holds %d entries; expected the UI's glyph set", len(defined))
	}

	for _, call := range regexp.MustCompile(`ic\(([^)]*)\)`).FindAllStringSubmatch(js, -1) {
		name := regexp.MustCompile(`'([a-z_]+)'`).FindStringSubmatch(call[1])
		if name != nil && !defined[name[1]] {
			t.Errorf("app.js draws ic(%q), which ICONS does not define", name[1])
		}
	}
	for _, name := range regexp.MustCompile(`data-icon="([a-z_]+)"`).FindAllStringSubmatch(html, -1) {
		if !defined[name[1]] {
			t.Errorf("index.html asks for data-icon %q, which ICONS does not define", name[1])
		}
	}
	for key := range defined {
		if strings.Contains(rest, "'"+key+"'") || strings.Contains(rest, "'ic:"+key+"'") ||
			strings.Contains(html, `data-icon="`+key+`"`) {
			continue
		}
		t.Errorf("ICONS defines %q, which nothing draws", key)
	}
}

func readUI(t *testing.T, name string) string {
	t.Helper()
	b, err := fs.ReadFile(uiFS, name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestSummaryFiguresMatchTheTileFilters: the state chips offer one chip per filter in app.js's
// TILES table, and a chip carries the key both the click handler and the counts read from it.
func TestSummaryFiguresMatchTheTileFilters(t *testing.T) {
	html := readUI(t, "ui/index.html")
	js := readUI(t, "ui/app.js")

	start := strings.Index(js, "const TILES = {")
	if start < 0 {
		t.Fatal("TILES table not found in app.js")
	}
	table := js[start:]
	table = table[:strings.Index(table, "};")]

	filters := 0
	for _, line := range strings.Split(table, "\n") {
		key, _, found := strings.Cut(strings.TrimSpace(line), ":")
		if found && regexp.MustCompile(`^[a-z]+$`).MatchString(key) {
			filters++
			if !strings.Contains(html, `data-tile="`+key+`"`) {
				t.Errorf("index.html has no state chip for the %q filter", key)
			}
		}
	}
	if filters < 2 {
		t.Fatalf("TILES holds %d filters; expected the summary's set", filters)
	}
	for _, m := range regexp.MustCompile(`data-tile="([a-z]*)"`).FindAllStringSubmatch(html, -1) {
		if m[1] == "" || strings.Contains(table, "\n  "+m[1]+": ") {
			continue // an empty key would clear the filter instead of naming one
		}
		t.Errorf("index.html offers the %q chip, which TILES does not filter by", m[1])
	}
}

// TestDashboardKnowsEveryErrorKind keeps app.js ERROR_KINDS in step with internal/errkind.
func TestDashboardKnowsEveryErrorKind(t *testing.T) {
	js, err := fs.ReadFile(uiFS, "ui/app.js")
	if err != nil {
		t.Fatal(err)
	}
	start := strings.Index(string(js), "const ERROR_KINDS = {")
	if start < 0 {
		t.Fatal("ERROR_KINDS table not found in app.js")
	}
	table := string(js)[start:]
	table = table[:strings.Index(table, "};")]
	for _, k := range errkind.All {
		if !strings.Contains(table, "\n  "+string(k)+": [") {
			t.Errorf("app.js ERROR_KINDS has no entry for %q", k)
		}
	}
}
