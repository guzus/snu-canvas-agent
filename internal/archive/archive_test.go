package archive

import (
	"context"
	"encoding/json"
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
	server        *httptest.Server
	filesTabOK    bool
	downloadCount int64
	courseListErr int
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
		writeJSON(w, []map[string]any{
			{"id": 101, "name": "자료구조 (2026-1)", "course_code": "M1522"},
		})
	})

	mux.HandleFunc("/api/v1/courses/101/files", func(w http.ResponseWriter, r *http.Request) {
		if !f.filesTabOK {
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprint(w, `{"status":"unauthorized","errors":[{"message":"disabled"}]}`)
			return
		}
		writeJSON(w, []map[string]any{fileJSON(11, "1주차 09/01 개요.pdf", 3, f.server.URL)})
	})

	mux.HandleFunc("/api/v1/courses/101/folders", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, []map[string]any{
			{"id": 3, "name": "1주차", "full_name": "course files/1주차"},
		})
	})

	mux.HandleFunc("/api/v1/courses/101/modules", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, []map[string]any{
			{"id": 1, "name": "Week 1", "items": []map[string]any{
				{"id": 900, "title": "강의노트", "type": "File", "content_id": 11},
				{"id": 901, "title": "실습자료", "type": "File", "content_id": 12},
				{"id": 902, "title": "외부링크", "type": "ExternalUrl"},
				{"id": 903, "title": "잠긴자료", "type": "File", "content_id": 13},
			}},
		})
	})

	mux.HandleFunc("/api/v1/courses/101/files/", func(w http.ResponseWriter, r *http.Request) {
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

	mux.HandleFunc("/api/v1/courses/101/assignments", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, []map[string]any{
			{"id": 5, "name": "과제1", "description": `<a href="/courses/101/files/77/download">첨부</a>`},
		})
	})

	mux.HandleFunc("/api/v1/announcements", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, []map[string]any{})
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
