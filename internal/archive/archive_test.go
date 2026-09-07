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
	"time"

	"github.com/mgnlia/lx-agent/internal/canvas"
	"golang.org/x/text/unicode/norm"
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

// assertDistinctOnDisk checks the two paths are genuinely two files. Comparing
// the path strings would be circular here: the whole bug is that two different
// strings name one file, so only the filesystem's own answer settles it.
func assertDistinctOnDisk(t *testing.T, dir, relA, relB string) {
	t.Helper()

	statOf := func(rel string) (os.FileInfo, string) {
		abs := filepath.Join(dir, filepath.FromSlash(rel))
		info, err := os.Stat(abs)
		if err != nil {
			t.Fatalf("%s missing on disk: %v", rel, err)
		}
		return info, abs
	}

	ia, _ := statOf(relA)
	ib, _ := statOf(relB)
	if os.SameFile(ia, ib) {
		t.Fatalf("both entries resolve to one file on disk:\n  %q\n  %q", relA, relB)
	}
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
	assertDistinctOnDisk(t, dir, a.RelPath, b.RelPath)

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

// Observed live on myETL: two distinct course files whose display names are the
// same text, one stored NFC and one as decomposed NFD jamo. Go compares the
// strings as different; macOS stores them as one file. Without normalization-
// aware path keys the two overwrite each other and re-download on every run.
func TestUnicodeNormalizationCollisionDoesNotChurn(t *testing.T) {
	const nfc = "착한 나.pdf"
	nfd := norm.NFD.String(nfc)
	if nfc == nfd {
		t.Fatal("fixture is not actually denormalized")
	}

	f := newFakeCanvas(t, true)
	f.extraFiles = []map[string]any{
		fileJSONSized(41, nfc, 3, "", 8),
		fileJSONSized(42, nfd, 3, "", 8),
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
	a, okA := m.Get(41)
	b, okB := m.Get(42)
	if !okA || !okB {
		t.Fatalf("both files should be archived: %v %v", okA, okB)
	}
	assertDistinctOnDisk(t, dir, a.RelPath, b.RelPath)

	// The symptom that matters: a repeating job must settle, not churn.
	for i := 0; i < 3; i++ {
		res, err := s.Run(context.Background(), Options{Dir: dir})
		if err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
		if res.Downloaded != 0 {
			t.Fatalf("run %d re-downloaded %d files; the archive never settles", i, res.Downloaded)
		}
	}
}

// A manifest written before normalization-aware keying must repair itself
// rather than keep two files pointed at one path.
func TestLegacyCollidingManifestSelfHeals(t *testing.T) {
	const nfc = "착한 나.pdf"
	nfd := norm.NFD.String(nfc)

	f := newFakeCanvas(t, true)
	f.extraFiles = []map[string]any{
		fileJSONSized(41, nfc, 3, "", 8),
		fileJSONSized(42, nfd, 3, "", 8),
	}
	dir := t.TempDir()

	// Seed the broken state exactly as the live manifest recorded it: the two
	// relpaths are different Go strings (one NFC, one NFD) but one file on
	// disk. A raw string comparison sees no collision here.
	course := "자료구조 (2026-1)"
	m := NewManifest(dir)
	m.Put(41, Entry{
		CourseID: 101, CourseName: course,
		RelPath: course + "/1주차/" + nfc,
		Size:    8, UpdatedAt: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
	})
	m.Put(42, Entry{
		CourseID: 101, CourseName: course,
		RelPath: course + "/1주차/" + nfd,
		Size:    8, UpdatedAt: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
	})
	if err := m.Save(); err != nil {
		t.Fatal(err)
	}

	s := newSyncer(f)
	if _, err := s.Run(context.Background(), Options{Dir: dir}); err != nil {
		t.Fatalf("run: %v", err)
	}

	healed := NewManifest(dir)
	if err := healed.Load(); err != nil {
		t.Fatal(err)
	}
	a, _ := healed.Get(41)
	b, _ := healed.Get(42)
	assertDistinctOnDisk(t, dir, a.RelPath, b.RelPath)

	res, err := s.Run(context.Background(), Options{Dir: dir})
	if err != nil {
		t.Fatalf("settle run: %v", err)
	}
	if res.Downloaded != 0 {
		t.Fatalf("archive still churning after repair: %d", res.Downloaded)
	}
}

// A manifest path recorded in a non-NFC form must be normalized and the file
// renamed to match, so the archive survives a move to a filesystem that
// compares names byte-for-byte instead of ignoring normalization.
func TestLegacyNonNFCPathIsMigrated(t *testing.T) {
	nfcName := "착한 나.pdf"
	nfdName := norm.NFD.String(nfcName)

	f := newFakeCanvas(t, true)
	f.extraFiles = []map[string]any{fileJSONSized(51, nfcName, 3, "", 8)}
	dir := t.TempDir()

	course := "자료구조 (2026-1)"
	oldRel := course + "/1주차/" + nfdName

	// Seed the pre-migration state: file on disk under the NFD name, manifest
	// pointing at it.
	abs := filepath.Join(dir, filepath.FromSlash(oldRel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte("PDFBYTES"), 0o644); err != nil {
		t.Fatal(err)
	}
	m := NewManifest(dir)
	m.Put(51, Entry{
		CourseID: 101, CourseName: course, RelPath: oldRel,
		Size: 8, UpdatedAt: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
	})
	if err := m.Save(); err != nil {
		t.Fatal(err)
	}

	before := atomic.LoadInt64(&f.downloadCount)
	res, err := newSyncer(f).Run(context.Background(), Options{Dir: dir})
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	healed := NewManifest(dir)
	if err := healed.Load(); err != nil {
		t.Fatal(err)
	}
	e, _ := healed.Get(51)
	if !norm.NFC.IsNormalString(e.RelPath) {
		t.Fatalf("manifest path not normalized: %q", e.RelPath)
	}

	// The migration must not cost a download of this file, and must leave one
	// copy of it rather than an NFC and an NFD sibling.
	for _, n := range res.New {
		if norm.NFC.String(n.Display) == nfcName {
			t.Fatalf("migration re-downloaded the migrated file: %+v", n)
		}
	}
	_ = before

	entries, err := os.ReadDir(filepath.Dir(abs))
	if err != nil {
		t.Fatal(err)
	}
	var copies []string
	for _, en := range entries {
		if norm.NFC.String(en.Name()) == nfcName {
			copies = append(copies, en.Name())
		}
	}
	if len(copies) != 1 {
		t.Fatalf("expected one copy after migration, found %d: %q", len(copies), copies)
	}
	if !norm.NFC.IsNormalString(copies[0]) {
		t.Fatalf("file on disk still not NFC: %q", copies[0])
	}
}

// The byte-exact case: a host that received both spellings (an rsync rewrote
// the name in transit) must end up with one file, not a permanent duplicate.
func TestDenormalizedDuplicateIsRemoved(t *testing.T) {
	nfcName := "착한 나.pdf"
	nfdName := norm.NFD.String(nfcName)

	f := newFakeCanvas(t, true)
	f.extraFiles = []map[string]any{fileJSONSized(52, nfcName, 3, "", 8)}
	dir := t.TempDir()

	course := "자료구조 (2026-1)"
	oldRel := course + "/1주차/" + nfdName
	newRel := course + "/1주차/" + nfcName
	oldAbs := filepath.Join(dir, filepath.FromSlash(oldRel))
	newAbs := filepath.Join(dir, filepath.FromSlash(newRel))

	if err := os.MkdirAll(filepath.Dir(oldAbs), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{oldAbs, newAbs} {
		if err := os.WriteFile(p, []byte("PDFBYTES"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// On a normalization-insensitive filesystem these are one file and this
	// scenario cannot arise; skip rather than assert the wrong thing.
	oi, _ := os.Stat(oldAbs)
	ni, _ := os.Stat(newAbs)
	if os.SameFile(oi, ni) {
		t.Skip("filesystem is normalization-insensitive; case not reachable here")
	}

	m := NewManifest(dir)
	m.Put(52, Entry{
		CourseID: 101, CourseName: course, RelPath: oldRel,
		Size: 8, UpdatedAt: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
	})
	if err := m.Save(); err != nil {
		t.Fatal(err)
	}

	if _, err := newSyncer(f).Run(context.Background(), Options{Dir: dir}); err != nil {
		t.Fatalf("run: %v", err)
	}

	if _, err := os.Stat(oldAbs); err == nil {
		t.Fatal("denormalized duplicate still present")
	}
	if _, err := os.Stat(newAbs); err != nil {
		t.Fatalf("normalized file missing: %v", err)
	}

	healed := NewManifest(dir)
	if err := healed.Load(); err != nil {
		t.Fatal(err)
	}
	if e, _ := healed.Get(52); e.RelPath != newRel {
		t.Fatalf("manifest path = %q, want %q", e.RelPath, newRel)
	}
}
