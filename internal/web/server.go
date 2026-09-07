package web

import (
	"embed"
	"fmt"
	"html/template"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

//go:embed templates/*.gohtml
var templatesFS embed.FS

// Server renders the archive as a browsable site.
type Server struct {
	store  *Store
	logger *slog.Logger
	pages  map[string]*template.Template
}

// pageData is what every template receives. Query is on the base struct so the
// header's search box can echo the current query on every page.
type pageData struct {
	Snap    *Snapshot
	Query   string
	Course  Course
	Results []File
}

func New(store *Store, logger *slog.Logger) (*Server, error) {
	funcs := template.FuncMap{
		"bytes": humanBytes,
		"date":  func(t time.Time) string { return t.Local().Format("2006-01-02") },
		"ago":   humanAgo,
	}

	pages := map[string]*template.Template{}
	for _, name := range []string{"index", "course", "search"} {
		t, err := template.New("layout.gohtml").Funcs(funcs).ParseFS(
			templatesFS, "templates/layout.gohtml", "templates/"+name+".gohtml")
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", name, err)
		}
		pages[name] = t
	}

	return &Server{store: store, logger: logger, pages: pages}, nil
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleIndex)
	mux.HandleFunc("/search", s.handleSearch)
	mux.HandleFunc("/course/", s.handleCourse)
	mux.HandleFunc("/file/", s.handleFile)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		snap := s.store.Snapshot()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"ok":true,"files":%d,"bytes":%d}`, snap.Count, snap.Size)
	})
	return s.logRequests(mux)
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	s.render(w, "index", pageData{Snap: s.store.Snapshot()})
}

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	snap := s.store.Snapshot()
	q := r.URL.Query().Get("q")
	s.render(w, "search", pageData{Snap: snap, Query: q, Results: snap.Search(q)})
}

func (s *Server) handleCourse(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/course/"))
	if err != nil {
		http.NotFound(w, r)
		return
	}

	snap := s.store.Snapshot()
	course, ok := snap.Course(id)
	if !ok {
		http.NotFound(w, r)
		return
	}
	s.render(w, "course", pageData{Snap: snap, Course: course})
}

// handleFile serves an archived file. The URL names a Canvas file ID, never a
// path: the path comes from the manifest, so no request can address a file
// outside the archive no matter what it sends.
func (s *Server) handleFile(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/file/"))
	if err != nil {
		http.NotFound(w, r)
		return
	}

	f, ok := s.store.Lookup(id)
	if !ok {
		http.NotFound(w, r)
		return
	}

	abs := filepath.Join(s.store.Dir(), filepath.FromSlash(f.RelPath))
	info, err := os.Stat(abs)
	if err != nil || info.IsDir() {
		http.Error(w, "file missing from archive", http.StatusNotFound)
		return
	}

	if ct := mime.TypeByExtension(filepath.Ext(abs)); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	w.Header().Set("Content-Disposition", contentDisposition(f.Name, r.URL.Query().Has("dl")))

	// ServeFile handles range requests, so a phone can seek a big PDF instead
	// of pulling the whole thing first.
	http.ServeFile(w, r, abs)
}

// contentDisposition keeps Korean filenames intact. The bare filename
// parameter must stay ASCII, so the real name goes in filename* (RFC 5987) and
// a stripped version fills the legacy slot.
func contentDisposition(name string, download bool) string {
	kind := "inline"
	if download {
		kind = "attachment"
	}

	ascii := strings.Map(func(r rune) rune {
		if r < 32 || r > 126 || r == '"' || r == '\\' {
			return '_'
		}
		return r
	}, name)

	return fmt.Sprintf("%s; filename=%q; filename*=UTF-8''%s",
		kind, ascii, url.PathEscape(name))
}

func (s *Server) render(w http.ResponseWriter, page string, data pageData) {
	t, ok := s.pages[page]
	if !ok {
		http.Error(w, "unknown page", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := t.Execute(w, data); err != nil {
		s.logger.Error("render", "page", page, "err", err)
	}
}

func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		s.logger.Info("http", "method", r.Method, "path", r.URL.Path,
			"remote", r.RemoteAddr, "dur", time.Since(start).Round(time.Millisecond))
	})
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for x := n / unit; x >= unit; x /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGT"[exp])
}

func humanAgo(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "방금"
	case d < time.Hour:
		return fmt.Sprintf("%d분 전", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%d시간 전", int(d.Hours()))
	default:
		return fmt.Sprintf("%d일 전", int(d.Hours()/24))
	}
}
