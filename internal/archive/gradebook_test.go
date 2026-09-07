package archive

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/mgnlia/lx-agent/internal/canvas"
)

func f64(v float64) *float64 { return &v }

func sampleSubs() []canvas.Submission {
	submitted := time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC)
	return []canvas.Submission{
		{
			AssignmentID: 1, Score: f64(54), SubmittedAt: &submitted, SubmissionType: "online_upload",
			Assignment: &canvas.Assignment{Name: "Homework 2", PointsPossible: 60,
				Description: "<p>Implement quicksort<br>and analyse it</p>"},
			Comments: []canvas.SubmissionComment{
				{AuthorName: "조교", Comment: "-6pt: QuickSort merge step is wrong."},
			},
		},
		{
			AssignmentID: 2, Score: f64(88), Assignment: &canvas.Assignment{Name: "Midterm Exam", PointsPossible: 100},
		},
		{
			AssignmentID: 3, Missing: true, Assignment: &canvas.Assignment{Name: "Homework 3", PointsPossible: 40},
		},
		{
			AssignmentID: 4, Body: "제 답안은 https://github.com/me/hw 입니다",
			Assignment: &canvas.Assignment{Name: "Essay", PointsPossible: 10},
		},
	}
}

func TestGradesReportCarriesScoresFeedbackAndInlineWork(t *testing.T) {
	course := canvas.Course{ID: 101, Name: "2024-2 Algorithms (002)"}
	grades := &canvas.Grades{CurrentScore: f64(92.72), FinalScore: f64(92.72)}

	out := string(renderGrades(course, grades, sampleSubs(), nil))

	for _, want := range []string{
		"2024-2 Algorithms (002) 성적",
		"92.72",
		"Homework 2", "54 / 60",
		"Midterm Exam", "88 / 100",
		"2026-04-01",
		"미제출",
		"피드백", "조교", "QuickSort merge step is wrong",
		"제출 내용", "github.com/me/hw",
		"과제 안내", "Implement quicksort",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("grades report missing %q", want)
		}
	}

	// Assignment briefs are course-authored HTML and are rendered, not escaped:
	// escaping printed raw markup at the reader.
	if strings.Contains(out, "&lt;p&gt;") {
		t.Error("assignment brief was escaped instead of rendered")
	}
	if !strings.Contains(out, "<br>") {
		t.Error("line break lost from the brief")
	}
}

func TestGradesReportWithoutGradesStillLists(t *testing.T) {
	out := string(renderGrades(canvas.Course{ID: 1, Name: "C"}, nil, sampleSubs(), nil))
	if !strings.Contains(out, "Homework 2") {
		t.Error("submissions missing when enrollment grades are unavailable")
	}
	if strings.Contains(out, "현재 점수") {
		t.Error("blank grade rows rendered")
	}
}

func TestGradesReportEscapesFeedback(t *testing.T) {
	subs := []canvas.Submission{{
		AssignmentID: 1,
		Assignment:   &canvas.Assignment{Name: `<img src=x onerror=alert(1)>`},
		Comments:     []canvas.SubmissionComment{{Comment: "<script>alert(1)</script>"}},
	}}
	out := string(renderGrades(canvas.Course{Name: "C"}, nil, subs, nil))

	if strings.Contains(out, "<script>alert(1)</script>") || strings.Contains(out, "<img src=x") {
		t.Fatal("report did not escape untrusted content")
	}
}

// Submission attachments carry a folder id from the submitter's own space,
// which must not decide where they land in the course tree.
func TestSubmissionFolderNaming(t *testing.T) {
	got := submissionFolder(canvas.Submission{
		AssignmentID: 7, Assignment: &canvas.Assignment{Name: "Homework 1-Part1"}})
	if got != "Homework 1-Part1" {
		t.Errorf("folder = %q", got)
	}
	if got := submissionFolder(canvas.Submission{AssignmentID: 7}); got != "assignment-7" {
		t.Errorf("fallback folder = %q", got)
	}
}

func TestGradesEntryIDCannotCollideWithSyllabus(t *testing.T) {
	course := 305840
	grades := gradesEntryID(course)
	for i := 1; i <= 40; i++ {
		if syllabusEntryID(course, i) == grades {
			t.Fatalf("syllabus slot %d collides with the grades slot", i)
		}
	}
	if grades >= 0 {
		t.Fatal("generated-document ids must be negative to stay out of the Canvas id space")
	}
}

// Assignment briefs link to files this tool has already archived. Those links
// must point at the local copy, or the document is useless without a live
// Canvas session — which is the situation the archive exists for.
func TestGradesReportRepointsFileLinksAtTheArchive(t *testing.T) {
	subs := []canvas.Submission{{
		AssignmentID: 1,
		Assignment: &canvas.Assignment{
			Name: "Homework 2",
			Description: `<p><a class="instructure_file_link" title="hw2.zip" ` +
				`href="https://myetl.snu.ac.kr/courses/296215/files/8219881/download?wrap=1" ` +
				`target="_blank">hw2.zip</a></p>` +
				`<p><a href="https://myetl.snu.ac.kr/courses/296215/files/999999/download">gone.zip</a></p>`,
		},
	}}

	archived := map[int]bool{8219881: true}
	resolve := func(id int) (string, bool) {
		if !archived[id] {
			return "", false
		}
		return "/file/" + strconv.Itoa(id), true
	}

	out := string(renderGrades(canvas.Course{Name: "C"}, nil, subs, resolve))

	if !strings.Contains(out, `href="/file/8219881"`) {
		t.Error("archived file link was not repointed at the local copy")
	}
	// A file that was never archived keeps its original URL, so a live session
	// can still reach it.
	if !strings.Contains(out, "files/999999/download") {
		t.Error("unarchived link should keep its original URL")
	}
	if strings.Contains(out, "instructure_file_link") {
		t.Error("class attribute should be dropped")
	}
	if !strings.Contains(out, ">hw2.zip<") {
		t.Error("link text lost")
	}
}
