package web

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mgnlia/lx-agent/internal/archive"
)

func testServer(t *testing.T) (*Server, *Store, string) {
	t.Helper()
	dir := t.TempDir()

	write := func(rel, body string) {
		abs := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	course := "2026-2 운영체제 (001)"
	write(course+"/1주차/개요.pdf", "PDFDATA")
	write(course+"/기말.pdf", "FINAL")

	m := archive.NewManifest(dir)
	m.Put(11, archive.Entry{
		CourseID: 101, CourseName: course, RelPath: course + "/1주차/개요.pdf",
		DisplayName: "개요.pdf", Size: 7,
		UpdatedAt: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
	})
	m.Put(12, archive.Entry{
		CourseID: 101, CourseName: course, RelPath: course + "/기말.pdf",
		DisplayName: "기말.pdf", Size: 5,
		UpdatedAt: time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC),
	})
	if err := m.Save(); err != nil {
		t.Fatal(err)
	}

	store := NewStore(dir)
	srv, err := New(store, slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})))
	if err != nil {
		t.Fatal(err)
	}
	return srv, store, dir
}

func get(t *testing.T, srv *Server, path string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	return w
}

func TestIndexListsCoursesByTerm(t *testing.T) {
	srv, _, _ := testServer(t)
	w := get(t, srv, "/")

	if w.Code != 200 {
		t.Fatalf("status %d", w.Code)
	}
	body := w.Body.String()
	for _, want := range []string{"2026-2", "운영체제", "/course/101"} {
		if !strings.Contains(body, want) {
			t.Errorf("index missing %q", want)
		}
	}
}

func TestCoursePageGroupsByFolder(t *testing.T) {
	srv, _, _ := testServer(t)
	body := get(t, srv, "/course/101").Body.String()

	if !strings.Contains(body, "1주차") {
		t.Error("folder heading missing")
	}
	for _, want := range []string{"/file/11", "/file/12", "개요.pdf", "기말.pdf"} {
		if !strings.Contains(body, want) {
			t.Errorf("course page missing %q", want)
		}
	}
}

func TestFileIsServedWithKoreanFilename(t *testing.T) {
	srv, _, _ := testServer(t)
	w := get(t, srv, "/file/11")

	if w.Code != 200 {
		t.Fatalf("status %d", w.Code)
	}
	if w.Body.String() != "PDFDATA" {
		t.Fatalf("body = %q", w.Body.String())
	}

	cd := w.Header().Get("Content-Disposition")
	if !strings.HasPrefix(cd, "inline;") {
		t.Errorf("want inline by default, got %q", cd)
	}
	// The RFC 5987 parameter must round-trip the Korean name exactly.
	_, enc, ok := strings.Cut(cd, "filename*=UTF-8''")
	if !ok {
		t.Fatalf("no filename* in %q", cd)
	}
	decoded, err := url.PathUnescape(enc)
	if err != nil || decoded != "개요.pdf" {
		t.Errorf("filename* decoded to %q (err %v)", decoded, err)
	}

	if dl := get(t, srv, "/file/11?dl=1").Header().Get("Content-Disposition"); !strings.HasPrefix(dl, "attachment;") {
		t.Errorf("?dl should force download, got %q", dl)
	}
}

// Files are addressed by Canvas ID and resolved through the manifest, so no
// request can name a path outside the archive however it is encoded.
func TestFileEndpointRejectsPathTraversal(t *testing.T) {
	srv, _, _ := testServer(t)

	for _, p := range []string{
		"/file/../../etc/passwd",
		"/file/..%2f..%2fetc%2fpasswd",
		"/file/999999",
		"/file/",
		"/file/0",
	} {
		w := get(t, srv, p)
		if w.Code == http.StatusOK {
			t.Errorf("%s returned 200 (%d bytes)", p, w.Body.Len())
		}
	}
}

func TestSearchMatchesKorean(t *testing.T) {
	srv, _, _ := testServer(t)
	body := get(t, srv, "/search?q="+url.QueryEscape("기말")).Body.String()

	if !strings.Contains(body, "/file/12") {
		t.Error("search did not find the Korean filename")
	}
	if strings.Contains(body, "/file/11") {
		t.Error("search matched an unrelated file")
	}
}

// New files must appear without restarting the server: the sync runs on its own
// schedule and nobody will remember to bounce the web service afterwards.
func TestSnapshotReloadsWhenManifestChanges(t *testing.T) {
	srv, store, dir := testServer(t)

	if got := store.Snapshot().Count; got != 2 {
		t.Fatalf("initial count = %d", got)
	}

	abs := filepath.Join(dir, "2026-2 운영체제 (001)", "새자료.pdf")
	if err := os.WriteFile(abs, []byte("NEW"), 0o644); err != nil {
		t.Fatal(err)
	}
	m := archive.NewManifest(dir)
	if err := m.Load(); err != nil {
		t.Fatal(err)
	}
	m.Put(13, archive.Entry{
		CourseID: 101, CourseName: "2026-2 운영체제 (001)",
		RelPath: "2026-2 운영체제 (001)/새자료.pdf", DisplayName: "새자료.pdf", Size: 3,
		UpdatedAt: time.Now().UTC(),
	})
	// Ensure the mtime moves even on a coarse-grained clock.
	time.Sleep(10 * time.Millisecond)
	if err := m.Save(); err != nil {
		t.Fatal(err)
	}

	if got := store.Snapshot().Count; got != 3 {
		t.Fatalf("count after sync = %d, want 3 (manifest change not picked up)", got)
	}
	if !strings.Contains(get(t, srv, "/course/101").Body.String(), "새자료.pdf") {
		t.Error("new file not visible without a restart")
	}
}

func TestHealthz(t *testing.T) {
	srv, _, _ := testServer(t)
	w := get(t, srv, "/healthz")

	var out struct {
		OK    bool  `json:"ok"`
		Files int   `json:"files"`
		Bytes int64 `json:"bytes"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("healthz not json: %v", err)
	}
	if !out.OK || out.Files != 2 {
		t.Fatalf("healthz = %+v", out)
	}
}
