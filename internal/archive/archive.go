package archive

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mgnlia/lx-agent/internal/canvas"
)

// ErrAuthExpired signals that the Canvas credential is dead. A scheduled run
// must surface this loudly: a cron that silently stops archiving is worse than
// no cron, because the user has already stopped downloading by hand.
var ErrAuthExpired = errors.New("canvas auth expired (re-issue the token or session cookie)")

// Options configures one archive run.
type Options struct {
	Dir           string
	Courses       []int // empty = every active course
	DryRun        bool
	MaxFileBytes  int64 // 0 = no limit
	IncludeVideos bool
	Concurrency   int
}

// FileResult describes one file the run acted on.
type FileResult struct {
	CourseName string
	Display    string
	RelPath    string
	Size       int64
	Updated    bool // replaced an older revision rather than being brand new
}

// CourseResult is the per-course rollup.
type CourseResult struct {
	CourseID   int
	CourseName string
	Found      int
	Downloaded int
	Skipped    int
	Failed     int
	Warnings   []string
}

// Result is the outcome of a full run.
type Result struct {
	Dir           string
	Courses       []CourseResult
	New           []FileResult
	Downloaded    int
	Skipped       int
	Failed        int
	SkippedLocked int
	SkippedTooBig int
	SkippedVideo  int
	Bytes         int64
	Errors        []string
	Duration      time.Duration
}

// Syncer mirrors Canvas course files to a local directory.
type Syncer struct {
	client *canvas.Client
	logger *slog.Logger
}

func New(client *canvas.Client, logger *slog.Logger) *Syncer {
	return &Syncer{client: client, logger: logger}
}

// enumeration is what one course's discovery pass found, plus enough signal to
// tell "this course genuinely has no files" apart from "every lookup failed".
type enumeration struct {
	candidates  []candidate
	folders     map[int]string
	warnings    []string
	authExpired bool
	sourcesOK   int
}

// candidate is one file discovered by any enumeration source.
type candidate struct {
	file     canvas.File
	source   string
	pathHint string // used when the file's folder is unknown (module-only files)
}

// fileLinkRe finds Canvas file references embedded in assignment and
// announcement HTML — the last place materials hide when both the Files tab
// and the Modules page are trimmed down.
var fileLinkRe = regexp.MustCompile(`/files/(\d+)`)

// Run archives every selected course. It never aborts on a single course's
// failure; only a dead credential stops the whole run.
func (s *Syncer) Run(ctx context.Context, opts Options) (*Result, error) {
	start := time.Now()

	if opts.Dir == "" {
		return nil, errors.New("archive dir is empty")
	}
	dir, err := expandDir(opts.Dir)
	if err != nil {
		return nil, err
	}
	opts.Dir = dir
	if opts.Concurrency <= 0 {
		opts.Concurrency = 3
	}

	if !opts.DryRun {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
	}

	manifest := NewManifest(dir)
	if err := manifest.Load(); err != nil {
		return nil, fmt.Errorf("load manifest: %w", err)
	}

	courses, err := s.selectCourses(ctx, opts.Courses)
	if err != nil {
		return nil, err
	}

	result := &Result{Dir: dir}
	dirNames := courseDirNames(courses)
	taken := manifest.TakenPaths()
	var takenMu sync.Mutex

	for _, course := range courses {
		cr := CourseResult{CourseID: course.ID, CourseName: course.Name}

		en := s.enumerate(ctx, course.ID)
		if en.authExpired {
			// Caught mid-run: the course list succeeded but the session died
			// before the files were read. Reporting a clean run here is the
			// failure that makes a scheduled archiver worthless.
			return nil, fmt.Errorf("%w (while reading %s)", ErrAuthExpired, course.Name)
		}
		cr.Warnings = en.warnings
		cr.Found = len(en.candidates)

		if en.sourcesOK == 0 {
			// Every discovery source errored, so "0 files" is not a fact about
			// the course. Count it as a failure so the run exits non-zero.
			cr.Failed++
			result.Failed++
			result.Errors = append(result.Errors,
				fmt.Sprintf("%s: every file source failed; course not archived", course.Name))
			result.Courses = append(result.Courses, cr)
			continue
		}

		plans := make([]plan, 0, len(en.candidates))
		for _, c := range en.candidates {
			p, skip, reason := s.planFile(course, dirNames[course.ID], en.folders, c, manifest, opts, taken)
			if skip {
				cr.Skipped++
				result.Skipped++
				switch reason {
				case "locked":
					result.SkippedLocked++
				case "toobig":
					result.SkippedTooBig++
				case "video":
					result.SkippedVideo++
				}
				continue
			}
			takenMu.Lock()
			taken[pathKey(p.rel)] = c.file.ID
			takenMu.Unlock()
			plans = append(plans, p)
		}

		s.download(ctx, plans, opts, manifest, result, &cr)

		for _, w := range cr.Warnings {
			result.Errors = append(result.Errors, fmt.Sprintf("%s: %s", course.Name, w))
		}
		result.Courses = append(result.Courses, cr)
	}

	if !opts.DryRun {
		if err := manifest.Save(); err != nil {
			return result, fmt.Errorf("save manifest: %w", err)
		}
	}

	result.Duration = time.Since(start)
	return result, nil
}

type plan struct {
	file       canvas.File
	courseID   int
	courseName string
	rel        string
	abs        string
	source     string
	updated    bool
}

func (s *Syncer) selectCourses(ctx context.Context, want []int) ([]canvas.Course, error) {
	courses, err := s.client.GetCourses(ctx)
	if err != nil {
		if canvas.IsAuthExpired(err) || canvas.IsForbidden(err) {
			return nil, fmt.Errorf("%w: %v", ErrAuthExpired, err)
		}
		return nil, fmt.Errorf("list courses: %w", err)
	}

	if len(want) == 0 {
		return courses, nil
	}

	keep := make(map[int]bool, len(want))
	for _, id := range want {
		keep[id] = true
	}
	var out []canvas.Course
	for _, c := range courses {
		if keep[c.ID] {
			out = append(out, c)
		}
	}
	return out, nil
}

// enumerate unions every place a course file can appear. Korean Canvas
// instances frequently disable the Files tab, in which case /files 403s and the
// materials are only reachable through module items — so a single source is not
// enough to call the archive complete.
func (s *Syncer) enumerate(ctx context.Context, courseID int) enumeration {
	en := enumeration{folders: make(map[int]string)}
	seen := make(map[int]bool)

	add := func(f canvas.File, source, hint string) {
		if f.ID == 0 || seen[f.ID] {
			return
		}
		seen[f.ID] = true
		en.candidates = append(en.candidates, candidate{file: f, source: source, pathHint: hint})
	}

	// note records a source's outcome. An expired credential is fatal to the
	// whole run; a 403 only means this source is closed, so keep going.
	note := func(label string, err error) bool {
		if err == nil {
			en.sourcesOK++
			return true
		}
		if canvas.IsAuthExpired(err) {
			en.authExpired = true
		}
		en.warnings = append(en.warnings, fmt.Sprintf("%s unavailable (%v)", label, shortErr(err)))
		return false
	}

	if folders, err := s.client.GetFolders(ctx, courseID); err != nil {
		// Auxiliary: folders only affect where files land, not whether they
		// are found, so this does not count toward sourcesOK.
		if canvas.IsAuthExpired(err) {
			en.authExpired = true
		}
		en.warnings = append(en.warnings, fmt.Sprintf("folders unavailable (%v)", shortErr(err)))
	} else {
		for _, f := range folders {
			en.folders[f.ID] = folderRelPath(f.FullName)
		}
	}

	files, err := s.client.GetFiles(ctx, courseID)
	if note("files tab", err) {
		for _, f := range files {
			add(f, "files", "")
		}
	}

	modules, err := s.client.GetModules(ctx, courseID)
	if note("modules", err) {
		for _, m := range modules {
			items := m.Items
			if len(items) == 0 {
				// Canvas omits inline items for large modules; fetch them.
				fetched, err := s.client.GetModuleItems(ctx, courseID, m.ID)
				if err != nil {
					if canvas.IsAuthExpired(err) {
						en.authExpired = true
					}
					en.warnings = append(en.warnings,
						fmt.Sprintf("module %q items unavailable (%v)", m.Name, shortErr(err)))
					continue
				}
				items = fetched
			}

			hint := path.Join("_modules", sanitizeSegment(m.Name))
			for _, item := range items {
				if !strings.EqualFold(item.Type, "File") || item.ContentID == 0 || seen[item.ContentID] {
					continue
				}
				f, err := s.client.GetFile(ctx, courseID, item.ContentID)
				if err != nil {
					if canvas.IsAuthExpired(err) {
						en.authExpired = true
					}
					en.warnings = append(en.warnings,
						fmt.Sprintf("module file %d unavailable (%v)", item.ContentID, shortErr(err)))
					continue
				}
				if f.DisplayName == "" && item.Title != "" {
					f.DisplayName = item.Title
				}
				add(*f, "module", hint)
			}
		}
	}

	for id := range s.embeddedFileIDs(ctx, courseID, note) {
		if seen[id] {
			continue
		}
		f, err := s.client.GetFile(ctx, courseID, id)
		if err != nil {
			continue // linked file may live in another course or be deleted
		}
		add(*f, "attachment", "_attachments")
	}

	sort.Slice(en.candidates, func(i, j int) bool { return en.candidates[i].file.ID < en.candidates[j].file.ID })
	return en
}

// embeddedFileIDs scrapes assignment and announcement bodies for file links.
func (s *Syncer) embeddedFileIDs(ctx context.Context, courseID int, note func(string, error) bool) map[int]bool {
	ids := make(map[int]bool)

	collect := func(html string) {
		for _, m := range fileLinkRe.FindAllStringSubmatch(html, -1) {
			if id, err := strconv.Atoi(m[1]); err == nil && id > 0 {
				ids[id] = true
			}
		}
	}

	assignments, err := s.client.GetAssignments(ctx, courseID)
	if note("assignments", err) {
		for _, a := range assignments {
			collect(a.Description)
		}
	}

	anns, err := s.client.GetAnnouncements(ctx, []int{courseID})
	if note("announcements", err) {
		for _, a := range anns {
			collect(a.Message)
		}
	}

	return ids
}

// planFile decides the destination path and whether the file needs fetching.
func (s *Syncer) planFile(
	course canvas.Course,
	courseDir string,
	folders map[int]string,
	c candidate,
	manifest *Manifest,
	opts Options,
	taken map[string]int,
) (plan, bool, string) {
	f := c.file

	if f.LockedForUser || f.URL == "" {
		return plan{}, true, "locked"
	}
	if isVideo(f) && !opts.IncludeVideos {
		return plan{}, true, "video"
	}
	if opts.MaxFileBytes > 0 && f.Size > opts.MaxFileBytes {
		return plan{}, true, "toobig"
	}

	sub := folders[f.FolderID]
	if sub == "" {
		sub = c.pathHint
	}

	rel := path.Join(courseDir, sub, fileName(f))
	if owner, ok := taken[pathKey(rel)]; ok && owner != f.ID {
		rel = disambiguate(rel, f.ID, taken)
	}

	updated := false
	if prev, ok := manifest.Get(f.ID); ok {
		rel = prev.RelPath // keep the path it was first archived at
		if !s.needsRefresh(opts.Dir, prev, f) {
			return plan{}, true, "current"
		}
		updated = true
	}

	return plan{
		file:       f,
		courseID:   course.ID,
		courseName: course.Name,
		rel:        rel,
		abs:        filepath.Join(opts.Dir, filepath.FromSlash(rel)),
		source:     c.source,
		updated:    updated,
	}, false, ""
}

// needsRefresh re-downloads only when Canvas reports a different revision or
// the local copy is missing or truncated.
func (s *Syncer) needsRefresh(dir string, prev Entry, f canvas.File) bool {
	abs := filepath.Join(dir, filepath.FromSlash(prev.RelPath))
	info, err := os.Stat(abs)
	if err != nil {
		return true
	}
	if prev.Size > 0 && info.Size() != prev.Size {
		return true
	}
	if f.Size > 0 && info.Size() != f.Size {
		return true
	}
	remote := fileRevision(f)
	if !remote.IsZero() && !prev.UpdatedAt.Equal(remote) {
		return true
	}
	return false
}

func (s *Syncer) download(ctx context.Context, plans []plan, opts Options, manifest *Manifest, result *Result, cr *CourseResult) {
	if len(plans) == 0 {
		return
	}

	if opts.DryRun {
		for _, p := range plans {
			result.Downloaded++
			cr.Downloaded++
			result.Bytes += p.file.Size
			result.New = append(result.New, FileResult{
				CourseName: p.courseName, Display: p.file.DisplayName,
				RelPath: p.rel, Size: p.file.Size, Updated: p.updated,
			})
		}
		return
	}

	sem := make(chan struct{}, opts.Concurrency)
	var wg sync.WaitGroup
	var mu sync.Mutex

	for _, p := range plans {
		wg.Add(1)
		go func(p plan) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			dlCtx, cancel := context.WithTimeout(ctx, 15*time.Minute)
			defer cancel()

			n, err := s.client.DownloadTo(dlCtx, p.file.URL, p.abs)
			if err == nil && p.file.Size > 0 && n != p.file.Size {
				// An expired file verifier makes Canvas serve a 200 OK login
				// page instead of the file. Without this check that HTML would
				// land on disk and be recorded as a complete download, so the
				// file would never be retried.
				os.Remove(p.abs)
				err = fmt.Errorf("size mismatch: got %d bytes, expected %d (auth or verifier likely expired)", n, p.file.Size)
			}

			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				cr.Failed++
				result.Failed++
				result.Errors = append(result.Errors,
					fmt.Sprintf("%s / %s: %v", p.courseName, p.file.DisplayName, shortErr(err)))
				s.logger.Warn("archive download failed", "course", p.courseName, "file", p.file.DisplayName, "err", err)
				return
			}

			manifest.Put(p.file.ID, Entry{
				CourseID:     p.courseID,
				CourseName:   p.courseName,
				RelPath:      p.rel,
				DisplayName:  p.file.DisplayName,
				Size:         n,
				UpdatedAt:    fileRevision(p.file),
				DownloadedAt: time.Now().UTC(),
				Source:       p.source,
			})

			cr.Downloaded++
			result.Downloaded++
			result.Bytes += n
			result.New = append(result.New, FileResult{
				CourseName: p.courseName, Display: p.file.DisplayName,
				RelPath: p.rel, Size: n, Updated: p.updated,
			})
			s.logger.Info("archived", "course", p.courseName, "file", p.rel, "bytes", n)
		}(p)
	}

	wg.Wait()
}

// courseDirNames assigns each course a unique directory name.
func courseDirNames(courses []canvas.Course) map[int]string {
	counts := make(map[string]int)
	for _, c := range courses {
		counts[courseBase(c)]++
	}

	out := make(map[int]string, len(courses))
	for _, c := range courses {
		base := courseBase(c)
		if counts[base] > 1 {
			base = fmt.Sprintf("%s (%d)", base, c.ID)
		}
		out[c.ID] = base
	}
	return out
}

func courseBase(c canvas.Course) string {
	if n := sanitizeSegment(c.Name); n != "" {
		return n
	}
	if n := sanitizeSegment(c.CourseCode); n != "" {
		return n
	}
	return fmt.Sprintf("course-%d", c.ID)
}

func fileName(f canvas.File) string {
	name := sanitizeSegment(f.DisplayName)
	if name == "" {
		if decoded, err := url.QueryUnescape(f.Filename); err == nil {
			name = sanitizeSegment(decoded)
		} else {
			name = sanitizeSegment(f.Filename)
		}
	}
	if name == "" {
		name = fmt.Sprintf("file-%d", f.ID)
	}
	return name
}

// fileRevision picks the timestamp Canvas bumps when a file's content changes.
func fileRevision(f canvas.File) time.Time {
	if !f.UpdatedAt.IsZero() {
		return f.UpdatedAt.UTC()
	}
	if !f.ModifiedAt.IsZero() {
		return f.ModifiedAt.UTC()
	}
	return time.Time{}
}

func isVideo(f canvas.File) bool {
	if strings.EqualFold(f.MimeClass, "video") || strings.EqualFold(f.MimeClass, "audio") {
		return true
	}
	ct := strings.ToLower(f.ContentType)
	return strings.HasPrefix(ct, "video/") || strings.HasPrefix(ct, "audio/")
}

func expandDir(dir string) (string, error) {
	if strings.HasPrefix(dir, "~/") || dir == "~" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		dir = filepath.Join(home, strings.TrimPrefix(dir, "~"))
	}
	return filepath.Abs(dir)
}

func shortErr(err error) string {
	s := err.Error()
	if len(s) > 160 {
		return s[:160] + "..."
	}
	return s
}

// Summary renders a human-readable one-screen report of the run.
func (r *Result) Summary() string {
	var b strings.Builder
	fmt.Fprintf(&b, "archive: %s\n", r.Dir)
	fmt.Fprintf(&b, "courses: %d  downloaded: %d  up-to-date: %d  failed: %d  (%s in %s)\n",
		len(r.Courses), r.Downloaded, r.Skipped, r.Failed, humanBytes(r.Bytes), r.Duration.Round(time.Second))
	if r.SkippedVideo > 0 || r.SkippedTooBig > 0 || r.SkippedLocked > 0 {
		fmt.Fprintf(&b, "skipped: %d locked, %d over size limit, %d media (use include_videos to fetch)\n",
			r.SkippedLocked, r.SkippedTooBig, r.SkippedVideo)
	}
	for _, c := range r.Courses {
		fmt.Fprintf(&b, "  %-45s found %3d  new %3d  failed %d\n",
			truncateName(c.CourseName, 45), c.Found, c.Downloaded, c.Failed)
	}
	if len(r.Errors) > 0 {
		b.WriteString("warnings:\n")
		for _, e := range r.Errors {
			fmt.Fprintf(&b, "  - %s\n", e)
		}
	}
	return b.String()
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
