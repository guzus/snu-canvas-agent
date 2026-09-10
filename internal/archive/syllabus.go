package archive

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/mgnlia/lx-agent/internal/canvas"
	"github.com/mgnlia/lx-agent/internal/syllabus"
)

// SyllabusDir is the per-course folder holding 강의계획서 artifacts.
const SyllabusDir = "강의계획서"

// syllabusEntryID maps a syllabus artifact to a manifest key.
//
// Manifest keys are Canvas file IDs, and syllabi have none — they come from a
// different system entirely. Negative IDs derived from the course ID give them
// a private key space that can never collide with a real Canvas file.
func syllabusEntryID(courseID, index int) int {
	return -(courseID*100 + index)
}

// gradesEntryID uses a reserved slot in that same negative space.
func gradesEntryID(courseID int) int { return -(courseID*100 + 90) }

// archiveSyllabus retrieves one course's 강의계획서 and writes it under the
// course directory.
//
// Canvas has no syllabus content for these courses: its syllabus_body is a
// shell around an iframe into sugang.snu.ac.kr. Most courses attach no file at
// all, so the rendered document is usually the only artifact — the raw record
// is stored beside it so a field this renderer misses is still recoverable.
func (s *Syncer) archiveSyllabus(
	ctx context.Context,
	client *syllabus.Client,
	course canvas.Course,
	courseDir string,
	manifest *Manifest,
	opts Options,
	result *Result,
	cr *CourseResult,
) string {
	body, err := s.client.GetSyllabusBody(ctx, course.ID)
	if err != nil {
		cr.Warnings = append(cr.Warnings, fmt.Sprintf("syllabus unavailable (%v)", shortErr(err)))
		return ""
	}

	ref, ok := syllabus.ParseRef(body)
	if !ok {
		cr.Warnings = append(cr.Warnings, "syllabus has no sugang reference")
		return ""
	}

	syl, err := client.Fetch(ctx, ref)
	if err != nil {
		cr.Warnings = append(cr.Warnings, fmt.Sprintf("syllabus fetch failed (%v)", shortErr(err)))
		return ""
	}

	base := path.Join(courseDir, SyllabusDir)
	idx := 0

	write := func(rel string, content []byte, label string) {
		idx++
		s.writeGenerated(course, rel, label, content, syllabusEntryID(course.ID, idx),
			"syllabus", manifest, opts, result, cr)
	}

	write(path.Join(base, "강의계획서.html"), syllabus.Render(course.Name, syl), "강의계획서.html")
	write(path.Join(base, "강의계획서.json"), syl.RawJSON, "강의계획서.json")

	// Attachments are small and the download is the only way to know whether
	// they changed, so they are fetched every run and kept only if different.
	for _, a := range syl.Attachments {
		idx++
		id := syllabusEntryID(course.ID, idx)
		name := sanitizeSegment(a.Name)
		if name == "" {
			continue
		}
		rel := path.Join(base, name)
		abs := filepath.Join(opts.Dir, filepath.FromSlash(rel))

		if opts.DryRun {
			result.Downloaded++
			cr.Downloaded++
			result.New = append(result.New, FileResult{CourseName: course.Name, Display: name, RelPath: rel})
			continue
		}

		tmp := abs + ".fetch"
		n, err := client.DownloadAttachment(ctx, a, tmp)
		if err != nil {
			s.recordFailure(result, cr, course.Name, name, err)
			continue
		}

		content, err := os.ReadFile(tmp)
		if err != nil {
			os.Remove(tmp)
			s.recordFailure(result, cr, course.Name, name, err)
			continue
		}
		if sameContent(abs, content) {
			os.Remove(tmp)
			cr.Skipped++
			result.Skipped++
			manifest.Put(id, generatedEntry(course, rel, name, "syllabus", n, content))
			continue
		}
		if err := os.Rename(tmp, abs); err != nil {
			os.Remove(tmp)
			s.recordFailure(result, cr, course.Name, name, err)
			continue
		}

		manifest.Put(id, generatedEntry(course, rel, name, "syllabus", n, content))
		cr.Downloaded++
		result.Downloaded++
		result.Bytes += n
		result.New = append(result.New, FileResult{
			CourseName: course.Name, Display: name, RelPath: rel, Size: n,
		})
		s.logger.Info("archived syllabus attachment", "course", course.Name, "file", rel, "bytes", n)
	}

	return syllabusRemarks(syl)
}

func syllabusRemarks(syl *syllabus.Syllabus) string {
	t3, _ := syl.Raw["LISTTAB03"].(map[string]any)
	if t3 == nil {
		return ""
	}
	for _, k := range []string{"ltPlanDocRemk", "ltPlanDocEngRemk"} {
		if s, ok := t3[k].(string); ok && strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}

func (s *Syncer) recordFailure(result *Result, cr *CourseResult, course, name string, err error) {
	cr.Failed++
	result.Failed++
	result.Errors = append(result.Errors, fmt.Sprintf("%s / %s: %v", course, name, shortErr(err)))
	s.logger.Warn("archive write failed", "course", course, "file", name, "err", err)
}

func generatedEntry(course canvas.Course, rel, name, source string, size int64, content []byte) Entry {
	sum := sha256.Sum256(content)
	return Entry{
		CourseID:     course.ID,
		CourseName:   course.Name,
		RelPath:      rel,
		DisplayName:  name,
		Size:         size,
		UpdatedAt:    time.Time{}, // no upstream revision timestamp
		DownloadedAt: time.Now().UTC(),
		Source:       source,
		Checksum:     hex.EncodeToString(sum[:]),
	}
}

// sameContent reports whether the file already holds exactly these bytes. The
// syllabus system exposes no revision timestamp, so content is the only signal
// for "unchanged".
func sameContent(abs string, content []byte) bool {
	existing, err := os.ReadFile(abs)
	if err != nil {
		return false
	}
	if len(existing) != len(content) {
		return false
	}
	return sha256.Sum256(existing) == sha256.Sum256(content)
}

// writeGenerated stores a document this tool produced rather than downloaded.
//
// These have no upstream revision timestamp, so an unchanged run is detected by
// comparing content — which also means a rerun rewrites nothing and the file's
// mtime stays meaningful.
func (s *Syncer) writeGenerated(
	course canvas.Course,
	rel, label string,
	content []byte,
	id int,
	source string,
	manifest *Manifest,
	opts Options,
	result *Result,
	cr *CourseResult,
) {
	if opts.DryRun {
		result.Downloaded++
		cr.Downloaded++
		result.New = append(result.New, FileResult{
			CourseName: course.Name, Display: label, RelPath: rel, Size: int64(len(content)),
		})
		return
	}

	abs := filepath.Join(opts.Dir, filepath.FromSlash(rel))
	if sameContent(abs, content) {
		cr.Skipped++
		result.Skipped++
		manifest.Put(id, generatedEntry(course, rel, label, source, int64(len(content)), content))
		return
	}

	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		s.recordFailure(result, cr, course.Name, label, err)
		return
	}
	if err := os.WriteFile(abs, content, 0o644); err != nil {
		s.recordFailure(result, cr, course.Name, label, err)
		return
	}

	manifest.Put(id, generatedEntry(course, rel, label, source, int64(len(content)), content))
	cr.Downloaded++
	result.Downloaded++
	result.Bytes += int64(len(content))
	result.New = append(result.New, FileResult{
		CourseName: course.Name, Display: label, RelPath: rel, Size: int64(len(content)),
	})
	s.logger.Info("archived document", "course", course.Name, "file", rel)
}
