package archive

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/mgnlia/lx-agent/internal/canvas"
)

// fakeCanvas mimics an SNU-style instance: the Files tab is disabled (403), so
// materials are only reachable through module items.
type fakeCanvas struct {
	server                *httptest.Server
	filesTabOK            bool
	downloadCount         int64
	courseListErr         int
	perCourseStatus       int // status returned by every per-course endpoint
	omitInlineModuleItems bool
	secondCourseExpired   bool
	noFilesTab            bool
	emptyEverything       bool
	extraFiles            []map[string]any
}

func newFakeCanvas(t *testing.T, filesTabOK bool) *fakeCanvas {
	t.Helper()
	f := &fakeCanvas{filesTabOK: filesTabOK}

	mux := http.NewServeMux()

	mux.HandleFunc("/api/v1/courses", func(w http.ResponseWriter, r *http.Request) {
		if f.courseListErr != 0 {
			w.WriteHeader(f.courseListErr)
			fmt.Fprint(w, `{"status":"unauthenticated"}`)
			return
		}
		courses := []map[string]any{
			{"id": 101, "name": "자료구조 (2026-1)", "course_code": "M1522"},
		}
		if f.secondCourseExpired {
			courses = append(courses, map[string]any{"id": 102, "name": "알고리즘 (2026-1)", "course_code": "M1523"})
		}
		writeJSON(w, courses)
	})

	gate := func(w http.ResponseWriter) bool {
		if f.perCourseStatus != 0 {
			w.WriteHeader(f.perCourseStatus)
			if f.perCourseStatus == http.StatusUnauthorized {
				fmt.Fprint(w, `{"status":"unauthenticated"}`)
			} else {
				fmt.Fprint(w, `{"status":"unauthorized"}`)
			}
			return false
		}
		return true
	}

	mux.HandleFunc("/api/v1/courses/101/files", func(w http.ResponseWriter, r *http.Request) {
		if !gate(w) {
			return
		}
		if !f.filesTabOK {
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprint(w, `{"status":"unauthorized","errors":[{"message":"disabled"}]}`)
			return
		}
		if f.emptyEverything {
			writeJSON(w, []map[string]any{})
			return
		}
		out := []map[string]any{fileJSON(11, "1주차 09/01 개요.pdf", 3, f.server.URL)}
		for _, e := range f.extraFiles {
			c := map[string]any{}
			for k, v := range e {
				c[k] = v
			}
			c["url"] = f.server.URL + fmt.Sprintf("/download/%v", c["id"])
			out = append(out, c)
		}
		writeJSON(w, out)
	})

	mux.HandleFunc("/api/v1/courses/101/folders", func(w http.ResponseWriter, r *http.Request) {
		if !gate(w) {
			return
		}
		writeJSON(w, []map[string]any{
			{"id": 3, "name": "1주차", "full_name": "course files/1주차"},
		})
	})

	moduleItems := []map[string]any{
		{"id": 900, "title": "강의노트", "type": "File", "content_id": 11},
		{"id": 901, "title": "실습자료", "type": "File", "content_id": 12},
		{"id": 902, "title": "외부링크", "type": "ExternalUrl"},
		{"id": 903, "title": "잠긴자료", "type": "File", "content_id": 13},
	}

	mux.HandleFunc("/api/v1/courses/101/modules", func(w http.ResponseWriter, r *http.Request) {
		if !gate(w) {
			return
		}
		if f.emptyEverything {
			writeJSON(w, []map[string]any{})
			return
		}
		m := map[string]any{"id": 1, "name": "Week 1"}
		if !f.omitInlineModuleItems {
			m["items"] = moduleItems
		}
		writeJSON(w, []map[string]any{m})
	})

	mux.HandleFunc("/api/v1/courses/101/modules/1/items", func(w http.ResponseWriter, r *http.Request) {
		if !gate(w) {
			return
		}
		writeJSON(w, moduleItems)
	})

	mux.HandleFunc("/api/v1/courses/101/files/", func(w http.ResponseWriter, r *http.Request) {
		if !gate(w) {
			return
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "/11"):
			writeJSON(w, fileJSON(11, "1주차 09/01 개요.pdf", 3, f.server.URL))
		case strings.HasSuffix(r.URL.Path, "/12"):
			writeJSON(w, fileJSON(12, "실습.zip", 0, f.server.URL))
		case strings.HasSuffix(r.URL.Path, "/13"):
			locked := fileJSON(13, "기말고사.pdf", 3, f.server.URL)
			locked["locked_for_user"] = true
			locked["url"] = ""
			writeJSON(w, locked)
		case strings.HasSuffix(r.URL.Path, "/77"):
			writeJSON(w, fileJSON(77, "공지첨부.hwp", 0, f.server.URL))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})

	mux.HandleFunc("/api/v1/courses/101/tabs", func(w http.ResponseWriter, r *http.Request) {
		tabs := []map[string]any{
			{"id": "home", "label": "홈", "type": "internal"},
			{"id": "context_external_tool_83", "label": "주차학습", "type": "external"},
		}
		if !f.noFilesTab {
			tabs = append(tabs, map[string]any{"id": "files", "label": "파일", "type": "internal"})
		}
		writeJSON(w, tabs)
	})

	mux.HandleFunc("/api/v1/courses/101/assignments", func(w http.ResponseWriter, r *http.Request) {
		if !gate(w) {
			return
		}
		if f.emptyEverything {
			writeJSON(w, []map[string]any{})
			return
		}
		writeJSON(w, []map[string]any{
			{"id": 5, "name": "과제1", "description": `<a href="/courses/101/files/77/download">첨부</a>`},
		})
	})

	mux.HandleFunc("/api/v1/announcements", func(w http.ResponseWriter, r *http.Request) {
		if !gate(w) {
			return
		}
		writeJSON(w, []map[string]any{})
	})

	// Every endpoint of the second course reports a dead session.
	mux.HandleFunc("/api/v1/courses/102/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"status":"unauthenticated"}`)
	})

	mux.HandleFunc("/download/", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&f.downloadCount, 1)
		fmt.Fprint(w, "PDFBYTES")
	})

	f.server = httptest.NewServer(mux)
	t.Cleanup(f.server.Close)
	return f
}

func fileJSON(id int, display string, folderID int, base string) map[string]any {
	return map[string]any{
		"id":           id,
		"folder_id":    folderID,
		"display_name": display,
		"filename":     display,
		"content-type": "application/pdf",
		"size":         8,
		"updated_at":   "2026-09-01T00:00:00Z",
		"url":          base + fmt.Sprintf("/download/%d", id),
	}
}

func fileJSONSized(id int, display string, folderID int, base string, size int64) map[string]any {
	f := fileJSON(id, display, folderID, base)
	f["size"] = size
	return f
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	// Mirror the "while(1);" CSRF prefix myETL prepends.
	fmt.Fprint(w, "while(1);")
	_ = json.NewEncoder(w).Encode(v)
}

func newSyncer(f *fakeCanvas) *Syncer {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	return New(canvas.NewClient(f.server.URL, "tok", logger), logger)
}

// Files hidden behind a disabled Files tab must still be archived via modules.
func TestRunFindsFilesWhenFilesTabDisabled(t *testing.T) {
	f := newFakeCanvas(t, false)
	dir := t.TempDir()

	res, err := newSyncer(f).Run(context.Background(), Options{Dir: dir, Concurrency: 2})
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	if res.Downloaded != 3 {
		t.Fatalf("downloaded = %d, want 3 (2 module files + 1 assignment attachment); errors=%v", res.Downloaded, res.Errors)
	}
	if res.SkippedLocked != 1 {
		t.Fatalf("skippedLocked = %d, want 1", res.SkippedLocked)
	}

	// "/" in the display name must become a path-safe segment, not a subdir.
	want := filepath.Join(dir, "자료구조 (2026-1)", "1주차", "1주차 09-01 개요.pdf")
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("expected %s: %v", want, err)
	}
	if b, _ := os.ReadFile(want); string(b) != "PDFBYTES" {
		t.Fatalf("content = %q", b)
	}

	// A module-only file with no known folder lands under its module name.
	if _, err := os.Stat(filepath.Join(dir, "자료구조 (2026-1)", "_modules", "Week 1", "실습.zip")); err != nil {
		t.Fatalf("module-hint path missing: %v", err)
	}
	// An assignment attachment is reachable only by scraping the HTML body.
	if _, err := os.Stat(filepath.Join(dir, "자료구조 (2026-1)", "_attachments", "공지첨부.hwp")); err != nil {
		t.Fatalf("attachment path missing: %v", err)
	}
}

// The second run is the one that matters for a cron: it must transfer nothing.
func TestSecondRunIsIdempotent(t *testing.T) {
	f := newFakeCanvas(t, true)
	dir := t.TempDir()
	s := newSyncer(f)

	if _, err := s.Run(context.Background(), Options{Dir: dir}); err != nil {
		t.Fatalf("first run: %v", err)
	}
	first := atomic.LoadInt64(&f.downloadCount)
	if first == 0 {
		t.Fatal("first run downloaded nothing")
	}

	res, err := s.Run(context.Background(), Options{Dir: dir})
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if res.Downloaded != 0 {
		t.Fatalf("second run downloaded %d files, want 0", res.Downloaded)
	}
	if got := atomic.LoadInt64(&f.downloadCount); got != first {
		t.Fatalf("download count grew from %d to %d on a no-op run", first, got)
	}
	if res.Skipped == 0 {
		t.Fatal("second run skipped nothing; manifest was not consulted")
	}
}

// A deleted local file must be re-fetched even though the manifest knows it.
func TestReDownloadsWhenLocalFileRemoved(t *testing.T) {
	f := newFakeCanvas(t, true)
	dir := t.TempDir()
	s := newSyncer(f)

	if _, err := s.Run(context.Background(), Options{Dir: dir}); err != nil {
		t.Fatalf("first run: %v", err)
	}
	target := filepath.Join(dir, "자료구조 (2026-1)", "1주차", "1주차 09-01 개요.pdf")
	if err := os.Remove(target); err != nil {
		t.Fatalf("remove: %v", err)
	}

	res, err := s.Run(context.Background(), Options{Dir: dir})
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if res.Downloaded != 1 {
		t.Fatalf("downloaded = %d, want 1", res.Downloaded)
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("file not restored: %v", err)
	}
}

// An expired credential must be distinguishable, not reported as a clean run.
func TestExpiredAuthIsSurfaced(t *testing.T) {
	f := newFakeCanvas(t, true)
	f.courseListErr = http.StatusUnauthorized

	_, err := newSyncer(f).Run(context.Background(), Options{Dir: t.TempDir()})
	if err == nil {
		t.Fatal("expected an error for expired auth")
	}
	if !strings.Contains(err.Error(), "auth expired") {
		t.Fatalf("error = %v, want it to identify expired auth", err)
	}
}

// Dry run must plan the same work without touching the disk.
func TestDryRunWritesNothing(t *testing.T) {
	f := newFakeCanvas(t, true)
	dir := t.TempDir()

	res, err := newSyncer(f).Run(context.Background(), Options{Dir: dir, DryRun: true})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Downloaded == 0 {
		t.Fatal("dry run planned no downloads")
	}
	if atomic.LoadInt64(&f.downloadCount) != 0 {
		t.Fatal("dry run performed real downloads")
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Fatalf("dry run wrote %d entries to disk", len(entries))
	}
}

// Canvas answers an expired file verifier with 200 OK and a login page. That
// must not be recorded as a complete download, or the file is never retried.
func TestShortBodyIsNotRecordedAsComplete(t *testing.T) {
	f := newFakeCanvas(t, true)
	dir := t.TempDir()

	// Serve a body shorter than the advertised size, as a login page would be.
	f.server.Config.Handler.(*http.ServeMux).HandleFunc("/download/short",
		func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "<html>login") })

	s := newSyncer(f)
	res, err := s.Run(context.Background(), Options{Dir: dir})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Downloaded == 0 {
		t.Fatal("baseline run downloaded nothing")
	}

	// Now corrupt one manifest entry's file to a short body and re-run.
	target := filepath.Join(dir, "자료구조 (2026-1)", "1주차", "1주차 09-01 개요.pdf")
	if err := os.WriteFile(target, []byte("<html>login"), 0o644); err != nil {
		t.Fatal(err)
	}

	res2, err := s.Run(context.Background(), Options{Dir: dir})
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if res2.Downloaded != 1 {
		t.Fatalf("truncated local file was not refetched: downloaded=%d", res2.Downloaded)
	}
	if b, _ := os.ReadFile(target); string(b) != "PDFBYTES" {
		t.Fatalf("file not repaired: %q", b)
	}
}

// macOS volumes are case-insensitive: two Canvas files differing only in case
// must not overwrite each other and then re-download in alternation forever.
func TestCaseOnlyNameCollisionGetsDistinctPaths(t *testing.T) {
	f := newFakeCanvas(t, true)
	f.extraFiles = []map[string]any{
		fileJSONSized(21, "Lecture.pdf", 3, "", 8),
		fileJSONSized(22, "lecture.pdf", 3, "", 8),
	}
	dir := t.TempDir()
	s := newSyncer(f)

	if _, err := s.Run(context.Background(), Options{Dir: dir}); err != nil {
		t.Fatalf("first run: %v", err)
	}

	m := NewManifest(dir)
	if err := m.Load(); err != nil {
		t.Fatal(err)
	}
	a, okA := m.Get(21)
	b, okB := m.Get(22)
	if !okA || !okB {
		t.Fatalf("both files should be archived: %v %v", okA, okB)
	}
	if strings.EqualFold(a.RelPath, b.RelPath) {
		t.Fatalf("case-only collision shares one on-disk path: %q vs %q", a.RelPath, b.RelPath)
	}

	// The real symptom of the bug: a second run re-downloads forever.
	before := atomic.LoadInt64(&f.downloadCount)
	res, err := s.Run(context.Background(), Options{Dir: dir})
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if res.Downloaded != 0 || atomic.LoadInt64(&f.downloadCount) != before {
		t.Fatalf("second run re-downloaded %d files", res.Downloaded)
	}
}

// Canvas omits inline module items for large modules; the archive must fall
// back to the module-items endpoint instead of silently finding nothing.
func TestModuleItemsFallbackWhenInlineItemsOmitted(t *testing.T) {
	f := newFakeCanvas(t, false)
	f.omitInlineModuleItems = true
	dir := t.TempDir()

	res, err := newSyncer(f).Run(context.Background(), Options{Dir: dir})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Downloaded != 3 {
		t.Fatalf("downloaded = %d, want 3 via the module-items endpoint; errors=%v", res.Downloaded, res.Errors)
	}
}

// A session that dies after the course list must abort, not report a clean run.
func TestAuthExpiringMidRunAborts(t *testing.T) {
	f := newFakeCanvas(t, true)
	f.perCourseStatus = http.StatusUnauthorized

	_, err := newSyncer(f).Run(context.Background(), Options{Dir: t.TempDir()})
	if err == nil {
		t.Fatal("expected an error when the session expires mid-run")
	}
	if !errors.Is(err, ErrAuthExpired) {
		t.Fatalf("error = %v, want ErrAuthExpired", err)
	}
}

// If every discovery source is closed, "0 files" is not a fact about the
// course — the run must report failure so the scheduled job exits non-zero.
func TestAllSourcesForbiddenIsAFailureNotAnEmptyRun(t *testing.T) {
	f := newFakeCanvas(t, true)
	f.perCourseStatus = http.StatusForbidden

	res, err := newSyncer(f).Run(context.Background(), Options{Dir: t.TempDir()})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Failed == 0 {
		t.Fatal("all sources forbidden was reported as a successful empty run")
	}
}

// A file literally named "x.pdf.part" must not be destroyed by the download of
// "x.pdf" writing its sidecar.
func TestPartSuffixedFileIsNotClobbered(t *testing.T) {
	f := newFakeCanvas(t, true)
	f.extraFiles = []map[string]any{
		fileJSONSized(31, "notes.pdf", 3, "", 8),
		fileJSONSized(32, "notes.pdf.part", 3, "", 8),
	}
	dir := t.TempDir()

	if _, err := newSyncer(f).Run(context.Background(), Options{Dir: dir}); err != nil {
		t.Fatalf("run: %v", err)
	}
	for _, name := range []string{"notes.pdf", "notes.pdf.part"} {
		p := filepath.Join(dir, "자료구조 (2026-1)", "1주차", name)
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("%s missing: %v", name, err)
		}
		if string(b) != "PDFBYTES" {
			t.Fatalf("%s content = %q", name, b)
		}
	}
}

// A session dying on a later course must not throw away the courses already
// downloaded — otherwise the next run refetches everything.
func TestManifestSurvivesMidRunAuthExpiry(t *testing.T) {
	f := newFakeCanvas(t, true)
	f.secondCourseExpired = true
	dir := t.TempDir()

	_, err := newSyncer(f).Run(context.Background(), Options{Dir: dir})
	if !errors.Is(err, ErrAuthExpired) {
		t.Fatalf("err = %v, want ErrAuthExpired", err)
	}

	m := NewManifest(dir)
	if err := m.Load(); err != nil {
		t.Fatalf("manifest: %v", err)
	}
	if m.Count() == 0 {
		t.Fatal("manifest lost every file downloaded before the session expired")
	}

	// The decisive check: a later run must not refetch what is already on disk.
	f.secondCourseExpired = false
	before := atomic.LoadInt64(&f.downloadCount)
	res, err := newSyncer(f).Run(context.Background(), Options{Dir: dir})
	if err != nil {
		t.Fatalf("recovery run: %v", err)
	}
	if res.Downloaded != 0 {
		t.Fatalf("recovery run re-downloaded %d files already archived", res.Downloaded)
	}
	if got := atomic.LoadInt64(&f.downloadCount); got != before {
		t.Fatalf("download count grew from %d to %d", before, got)
	}
}

// SNU courses with the Files tab removed answer /files with 200 and an empty
// array, so an empty course must be explained rather than reported as a clean
// archive of nothing.
func TestEmptyCourseWithoutFilesTabIsReportedUnreachable(t *testing.T) {
	f := newFakeCanvas(t, true)
	f.noFilesTab = true
	f.emptyEverything = true

	res, err := newSyncer(f).Run(context.Background(), Options{Dir: t.TempDir()})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(res.Unreachable) != 1 {
		t.Fatalf("Unreachable = %v, want one entry explaining the empty course", res.Unreachable)
	}
	if !strings.Contains(res.Unreachable[0], "주차학습") {
		t.Fatalf("explanation should name the LTI tool holding the materials: %q", res.Unreachable[0])
	}
	if !strings.Contains(res.Summary(), "not archivable via the Canvas API") {
		t.Fatal("summary hides the gap")
	}
}

// A course that is genuinely empty but still exposes its Files tab is not a gap.
func TestGenuinelyEmptyCourseIsNotFlagged(t *testing.T) {
	f := newFakeCanvas(t, true)
	f.emptyEverything = true

	res, err := newSyncer(f).Run(context.Background(), Options{Dir: t.TempDir()})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(res.Unreachable) != 0 {
		t.Fatalf("Unreachable = %v, want none", res.Unreachable)
	}
}
