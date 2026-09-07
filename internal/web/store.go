// Package web serves the archived course files as a browsable site over the
// tailnet.
package web

import (
	"os"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mgnlia/lx-agent/internal/archive"
)

// File is one archived file as the templates see it.
type File struct {
	ID      int
	Name    string
	RelPath string
	Course  string
	Folder  string
	Size    int64
	Updated time.Time
	Ext     string
}

// Folder groups files as they were laid out in the LMS.
type Folder struct {
	Name  string // "" is the course root
	Files []File
}

// Course is one course's worth of archived material.
type Course struct {
	ID      int
	Name    string
	Term    string
	Dir     string
	Folders []Folder
	Count   int
	Size    int64
	Latest  time.Time
}

// Snapshot is an immutable view of the archive, rebuilt when the manifest
// changes on disk.
type Snapshot struct {
	Courses  []Course
	Terms    []TermGroup
	ByID     map[int]File
	Count    int
	Size     int64
	Latest   time.Time
	SyncedAt time.Time
}

// TermGroup buckets courses by semester, which is how the index reads best:
// twenty flat course names are a wall, four terms are a table of contents.
type TermGroup struct {
	Term    string
	Courses []Course
	Count   int
	Size    int64
}

// Store reloads the manifest whenever it changes, so files appear in the UI
// after a sync without restarting the server.
type Store struct {
	dir string

	mu      sync.RWMutex
	snap    *Snapshot
	modTime time.Time
	size    int64
}

func NewStore(dir string) *Store {
	return &Store{dir: dir, snap: &Snapshot{ByID: map[int]File{}}}
}

// Dir is the archive root.
func (s *Store) Dir() string { return s.dir }

// Snapshot returns the current view, reloading first if the manifest changed.
func (s *Store) Snapshot() *Snapshot {
	m := archive.NewManifest(s.dir)

	info, err := os.Stat(m.Path())
	if err == nil {
		s.mu.RLock()
		fresh := info.ModTime().Equal(s.modTime) && info.Size() == s.size
		snap := s.snap
		s.mu.RUnlock()
		if fresh && snap != nil {
			return snap
		}
	}

	if err := m.Load(); err != nil {
		s.mu.RLock()
		defer s.mu.RUnlock()
		return s.snap
	}

	snap := build(m.Snapshot())

	s.mu.Lock()
	defer s.mu.Unlock()
	s.snap = snap
	if info != nil {
		s.modTime = info.ModTime()
		s.size = info.Size()
		snap.SyncedAt = info.ModTime()
	}
	return snap
}

// Lookup resolves a Canvas file ID. Serving is keyed by ID rather than by a
// path from the URL, so a request cannot name a file outside the archive.
func (s *Store) Lookup(id int) (File, bool) {
	snap := s.Snapshot()
	f, ok := snap.ByID[id]
	return f, ok
}

// Search matches on file and course name, case-insensitively.
func (snap *Snapshot) Search(q string) []File {
	q = strings.ToLower(strings.TrimSpace(q))
	if q == "" {
		return nil
	}

	var out []File
	for _, c := range snap.Courses {
		for _, fo := range c.Folders {
			for _, f := range fo.Files {
				if strings.Contains(strings.ToLower(f.Name), q) ||
					strings.Contains(strings.ToLower(f.Course), q) ||
					strings.Contains(strings.ToLower(f.Folder), q) {
					out = append(out, f)
				}
			}
		}
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Updated.After(out[j].Updated) })
	if len(out) > 300 {
		out = out[:300]
	}
	return out
}

// Course finds a course by ID.
func (snap *Snapshot) Course(id int) (Course, bool) {
	for _, c := range snap.Courses {
		if c.ID == id {
			return c, true
		}
	}
	return Course{}, false
}

func build(entries map[int]archive.Entry) *Snapshot {
	snap := &Snapshot{ByID: make(map[int]File, len(entries))}

	type acc struct {
		course  Course
		folders map[string][]File
	}
	byCourse := map[int]*acc{}

	for id, e := range entries {
		courseDir, folder, name := splitRel(e.RelPath)

		f := File{
			ID:      id,
			Name:    displayName(e, name),
			RelPath: e.RelPath,
			Course:  e.CourseName,
			Folder:  folder,
			Size:    e.Size,
			Updated: e.UpdatedAt,
			Ext:     strings.TrimPrefix(strings.ToLower(path.Ext(name)), "."),
		}
		snap.ByID[id] = f

		a := byCourse[e.CourseID]
		if a == nil {
			a = &acc{
				course:  Course{ID: e.CourseID, Name: e.CourseName, Term: termOf(e.CourseName), Dir: courseDir},
				folders: map[string][]File{},
			}
			byCourse[e.CourseID] = a
		}
		a.folders[folder] = append(a.folders[folder], f)
		a.course.Count++
		a.course.Size += e.Size
		if e.UpdatedAt.After(a.course.Latest) {
			a.course.Latest = e.UpdatedAt
		}

		snap.Count++
		snap.Size += e.Size
		if e.UpdatedAt.After(snap.Latest) {
			snap.Latest = e.UpdatedAt
		}
	}

	for _, a := range byCourse {
		names := make([]string, 0, len(a.folders))
		for n := range a.folders {
			names = append(names, n)
		}
		sort.Strings(names)

		for _, n := range names {
			files := a.folders[n]
			sort.Slice(files, func(i, j int) bool { return files[i].Name < files[j].Name })
			a.course.Folders = append(a.course.Folders, Folder{Name: n, Files: files})
		}
		snap.Courses = append(snap.Courses, a.course)
	}

	// Newest term first: the course you need is almost always the current one.
	sort.Slice(snap.Courses, func(i, j int) bool {
		if snap.Courses[i].Term != snap.Courses[j].Term {
			return snap.Courses[i].Term > snap.Courses[j].Term
		}
		return snap.Courses[i].Name < snap.Courses[j].Name
	})

	for _, c := range snap.Courses {
		n := len(snap.Terms)
		if n == 0 || snap.Terms[n-1].Term != c.Term {
			snap.Terms = append(snap.Terms, TermGroup{Term: c.Term})
			n++
		}
		g := &snap.Terms[n-1]
		g.Courses = append(g.Courses, c)
		g.Count += c.Count
		g.Size += c.Size
	}

	return snap
}

// splitRel breaks "course dir/folder/sub/file.pdf" into its parts.
func splitRel(rel string) (courseDir, folder, name string) {
	rel = strings.TrimPrefix(rel, "/")
	parts := strings.Split(rel, "/")
	switch len(parts) {
	case 0:
		return "", "", ""
	case 1:
		return "", "", parts[0]
	default:
		return parts[0], strings.Join(parts[1:len(parts)-1], "/"), parts[len(parts)-1]
	}
}

func displayName(e archive.Entry, fallback string) string {
	if n := strings.TrimSpace(e.DisplayName); n != "" {
		return n
	}
	return fallback
}

// termOf pulls the leading "2026-2" from a course name. SNU course names carry
// the semester, which is the only grouping the manifest can support.
func termOf(courseName string) string {
	name := strings.TrimSpace(courseName)
	if len(name) < 6 {
		return "기타"
	}
	head, _, _ := strings.Cut(name, " ")
	year, sem, ok := strings.Cut(head, "-")
	if !ok || len(year) != 4 || len(sem) == 0 {
		return "기타"
	}
	for _, r := range year + sem {
		if r < '0' || r > '9' {
			return "기타"
		}
	}
	return head
}
