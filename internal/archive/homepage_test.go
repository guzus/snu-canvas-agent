package archive

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseHomepageURLs(t *testing.T) {
	text := `* 정원 외 신청 기간 중 승인 예정.
* 과목 홈페이지: https://csl.snu.ac.kr/courses/4190.307/2026-2/
* 수강생은 Linux나 MacOS 운영체제 상에서 C 프로그래밍이 가능해야 함.`
	got := parseHomepageURLs(text)
	if len(got) != 1 || got[0] != "https://csl.snu.ac.kr/courses/4190.307/2026-2/" {
		t.Fatalf("got %v", got)
	}
}

func TestParseHomepageURLsEnglishAndPunctuation(t *testing.T) {
	text := `Course homepage: https://example.com/os/2026-2/).`
	got := parseHomepageURLs(text)
	if len(got) != 1 || got[0] != "https://example.com/os/2026-2/" {
		t.Fatalf("got %v", got)
	}
}

func TestParseHomepageURLsIgnoresUnlabeled(t *testing.T) {
	text := `See https://docs.google.com/forms/d/xyz and https://sys.snu.ac.kr for submission.`
	if got := parseHomepageURLs(text); len(got) != 0 {
		t.Fatalf("unlabeled URLs should be ignored, got %v", got)
	}
}

func TestHomepageEntryIDIsStable(t *testing.T) {
	got := homepageEntryID(305829, "https://csl.snu.ac.kr/courses/4190.307/2026-2/0-overview.pdf")
	if got != -1285412845 {
		t.Fatalf("id = %d, want -1285412845 (changing this remaps the archive)", got)
	}
}

func TestHomepageEntryIDIncludesCourse(t *testing.T) {
	url := "https://csl.snu.ac.kr/courses/4190.307/2026-2/0-overview.pdf"
	if homepageEntryID(101, url) == homepageEntryID(102, url) {
		t.Fatal("two courses sharing a homepage must not share a manifest id")
	}
}

func TestHomepageEntryIDStaysOutOfOtherKeySpaces(t *testing.T) {
	course := 305829
	id := homepageEntryID(course, "https://csl.snu.ac.kr/courses/4190.307/2026-2/0-overview.pdf")
	if id >= -1_000_000_000 || id <= -2_073_741_823 {
		t.Fatalf("id %d is outside the homepage range", id)
	}
	if id == gradesEntryID(course) {
		t.Fatal("homepage id collided with grades")
	}
	for i := 1; i <= 99; i++ {
		if id == syllabusEntryID(course, i) {
			t.Fatalf("homepage id collided with syllabus slot %d", i)
		}
	}
}

func TestHomepageDirTreatsIndexHTMLAsDirectory(t *testing.T) {
	u, err := url.Parse("https://csl.snu.ac.kr/courses/os/index.html")
	if err != nil {
		t.Fatal(err)
	}
	if got := homepageDir(u); got != "/courses/os" {
		t.Fatalf("homepageDir = %q", got)
	}
	slide, _ := url.Parse("https://csl.snu.ac.kr/courses/os/slides.pdf")
	if !underPathPrefix(slide, u) {
		t.Fatal("slides.pdf next to index.html must be in scope")
	}
}

func TestRelUnderHomeKeepsNestedPath(t *testing.T) {
	home, _ := url.Parse("https://csl.snu.ac.kr/courses/os/2026-2/")
	nested, _ := url.Parse("https://csl.snu.ac.kr/courses/os/2026-2/lectures/intro.pdf")
	if got := relUnderHome(nested, home); got != "lectures/intro.pdf" {
		t.Fatalf("rel = %q", got)
	}
}

func TestValidHomepageRejectsLabRoot(t *testing.T) {
	if _, err := validHomepage("https://csl.snu.ac.kr/"); err == nil {
		t.Fatal("lab root should be refused")
	}
	if _, err := validHomepage("https://csl.snu.ac.kr/courses/4190.307/2026-2/"); err != nil {
		t.Fatalf("course path should be accepted: %v", err)
	}
}

func TestDiscoverKeepsSamePathDocsOnly(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/courses/os/2026-2/", func(w http.ResponseWriter, r *http.Request) {
		switch strings.TrimPrefix(r.URL.Path, "/courses/os/2026-2/") {
		case "", "index.html":
			fmt.Fprint(w, `<html><a href="a.pdf">Overview</a>`+
				`<a href="news">News</a>`+
				`<a href="/other.pdf">same host other path</a>`+
				`<a href="https://pages.cs.wisc.edu/~remzi/OSTEP/intro.pdf">OSTEP</a></html>`)
		case "news", "news/":
			fmt.Fprint(w, `<html><a href="b.pdf">More</a></html>`)
		case "a.pdf":
			w.Header().Set("Content-Type", "application/pdf")
			fmt.Fprint(w, "%PDF-A")
		case "b.pdf":
			w.Header().Set("Content-Type", "application/pdf")
			fmt.Fprint(w, "%PDF-B")
		default:
			http.NotFound(w, r)
		}
	})
	mux.HandleFunc("/other.pdf", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "%PDF-NO")
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	home, err := url.Parse(srv.URL + "/courses/os/2026-2/")
	if err != nil {
		t.Fatal(err)
	}
	disc, err := discoverHomepage(context.Background(), srv.Client(), home)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}

	names := map[string]bool{}
	for _, f := range disc.files {
		names[f.Name] = true
	}
	if !names["a.pdf"] || !names["b.pdf"] {
		t.Fatalf("files = %+v, want a.pdf and b.pdf (news page must be crawled)", disc.files)
	}
	if names["other.pdf"] {
		t.Fatalf("crawled off-path file: %+v", disc.files)
	}

	var sawOSTEP bool
	for _, e := range disc.external {
		if strings.Contains(e.URL, "OSTEP") {
			sawOSTEP = true
		}
	}
	if !sawOSTEP {
		t.Fatalf("off-site reading should be noted, got %+v", disc.external)
	}
}

func TestRunArchivesHomepage(t *testing.T) {
	f := newFakeCanvas(t, true)
	site := newFakeHomepage(t)
	dir := t.TempDir()

	res, err := newSyncer(f).Run(context.Background(), Options{
		Dir:         dir,
		Concurrency: 2,
		Homepages:   map[int]string{101: site.URL + "/courses/os/2026-2/"},
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	base := filepath.Join(dir, "자료구조 (2026-1)", HomepageDir)
	for _, name := range []string{"a.pdf", "b.pdf", "homepage.html", "homepage.json", "index.html"} {
		if _, err := os.Stat(filepath.Join(base, name)); err != nil {
			t.Fatalf("%s missing: %v (errors=%v)", name, err, res.Errors)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(base, "a.pdf")); string(b) != "%PDF-A" {
		t.Fatalf("a.pdf content = %q", b)
	}
	if _, err := os.Stat(filepath.Join(base, "other.pdf")); err == nil {
		t.Fatal("off-path other.pdf should not have been archived")
	}

	note, _ := os.ReadFile(filepath.Join(base, "homepage.json"))
	if !strings.Contains(string(note), "a.pdf") {
		t.Fatalf("homepage.json missing file list: %s", note)
	}
}

func TestHomepageRejectsOffPathRedirect(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/courses/os/2026-2/", func(w http.ResponseWriter, r *http.Request) {
		switch strings.TrimPrefix(r.URL.Path, "/courses/os/2026-2/") {
		case "", "index.html":
			fmt.Fprint(w, `<html><a href="a.pdf">A</a></html>`)
		case "a.pdf":
			http.Redirect(w, r, "/other.pdf", http.StatusFound)
		default:
			http.NotFound(w, r)
		}
	})
	mux.HandleFunc("/other.pdf", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "%PDF-NO")
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	f := newFakeCanvas(t, true)
	dir := t.TempDir()
	res, err := newSyncer(f).Run(context.Background(), Options{
		Dir: dir, Concurrency: 2,
		Homepages: map[int]string{101: srv.URL + "/courses/os/2026-2/"},
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "자료구조 (2026-1)", HomepageDir, "a.pdf")); statErr == nil {
		t.Fatal("redirected off-path PDF should not have been archived")
	}
	if res.Failed == 0 {
		t.Fatalf("expected a homepage fetch failure, errors=%v", res.Errors)
	}
}

func TestHomepageRejectsHTMLMasqueradingAsPDF(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/courses/os/2026-2/", func(w http.ResponseWriter, r *http.Request) {
		switch strings.TrimPrefix(r.URL.Path, "/courses/os/2026-2/") {
		case "", "index.html":
			fmt.Fprint(w, `<html><a href="a.pdf">A</a></html>`)
		case "a.pdf":
			fmt.Fprint(w, "<!-- missing --><h1>not a pdf</h1>")
		default:
			http.NotFound(w, r)
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	f := newFakeCanvas(t, true)
	dir := t.TempDir()
	_, err := newSyncer(f).Run(context.Background(), Options{
		Dir: dir, Concurrency: 2,
		Homepages: map[int]string{101: srv.URL + "/courses/os/2026-2/"},
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "자료구조 (2026-1)", HomepageDir, "a.pdf")); statErr == nil {
		t.Fatal("HTML body must not be saved as a.pdf")
	}
}

func TestHomepageHonorsMaxFileBytes(t *testing.T) {
	f := newFakeCanvas(t, true)
	site := newFakeHomepage(t)
	dir := t.TempDir()
	res, err := newSyncer(f).Run(context.Background(), Options{
		Dir:          dir,
		Concurrency:  2,
		MaxFileBytes: 3, // "%PDF-A" is 6 bytes
		Homepages:    map[int]string{101: site.URL + "/courses/os/2026-2/"},
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.SkippedTooBig == 0 {
		t.Fatalf("expected oversized homepage files to be skipped, errors=%v", res.Errors)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "자료구조 (2026-1)", HomepageDir, "a.pdf")); statErr == nil {
		t.Fatal("oversize a.pdf should not have been kept")
	}
}

func TestHomepageSecondRunIsIdempotent(t *testing.T) {
	f := newFakeCanvas(t, true)
	site := newFakeHomepage(t)
	dir := t.TempDir()
	opts := Options{
		Dir:         dir,
		Concurrency: 2,
		Homepages:   map[int]string{101: site.URL + "/courses/os/2026-2/"},
	}
	s := newSyncer(f)

	first, err := s.Run(context.Background(), opts)
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	if first.Downloaded == 0 {
		t.Fatal("first run downloaded nothing")
	}

	second, err := s.Run(context.Background(), opts)
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if second.Downloaded != 0 {
		t.Fatalf("second run downloaded %d, want 0 (errors=%v)", second.Downloaded, second.Errors)
	}
}

func newFakeHomepage(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/courses/os/2026-2/", func(w http.ResponseWriter, r *http.Request) {
		switch strings.TrimPrefix(r.URL.Path, "/courses/os/2026-2/") {
		case "", "index.html":
			fmt.Fprint(w, `<html><a href="a.pdf">A</a><a href="news">News</a></html>`)
		case "news", "news/":
			fmt.Fprint(w, `<html><a href="b.pdf">B</a></html>`)
		case "a.pdf":
			fmt.Fprint(w, "%PDF-A")
		case "b.pdf":
			fmt.Fprint(w, "%PDF-B")
		default:
			http.NotFound(w, r)
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}
