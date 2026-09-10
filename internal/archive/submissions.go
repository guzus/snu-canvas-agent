package archive

import (
	"context"
	"fmt"
	"path"
	"strings"

	"github.com/mgnlia/lx-agent/internal/canvas"
)

// SubmissionsDir holds the student's own submitted work inside a course.
const SubmissionsDir = "제출물"

// submissionFolder names the per-assignment folder for submitted files.
func submissionFolder(s canvas.Submission) string {
	if s.Assignment != nil && strings.TrimSpace(s.Assignment.Name) != "" {
		return s.Assignment.Name
	}
	return fmt.Sprintf("assignment-%d", s.AssignmentID)
}

// archiveGrades writes the per-course report of scores and feedback.
//
// The submitted files are archived through the normal file path (they are real
// Canvas files with real ids); this covers what has no file behind it — scores,
// instructor comments, and text or URL submissions.
func (s *Syncer) archiveGrades(
	ctx context.Context,
	course canvas.Course,
	courseDir string,
	subs []canvas.Submission,
	manifest *Manifest,
	opts Options,
	result *Result,
	cr *CourseResult,
) {
	grades, err := s.client.GetSelfGrades(ctx, course.ID)
	if err != nil {
		cr.Warnings = append(cr.Warnings, fmt.Sprintf("grades unavailable (%v)", shortErr(err)))
	}

	// Resolve Canvas file links against the manifest so a brief that links
	// hw2.zip opens the archived hw2.zip instead of requiring a live session.
	resolve := func(fileID int) (string, bool) {
		if _, ok := manifest.Get(fileID); !ok {
			return "", false
		}
		return fmt.Sprintf("/file/%d", fileID), true
	}

	rel := path.Join(courseDir, SubmissionsDir, GradesFile)
	s.writeGenerated(course, rel, GradesFile, renderGrades(course, grades, subs, resolve),
		gradesEntryID(course.ID), "grades", manifest, opts, result, cr)
}
