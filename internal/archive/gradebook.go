package archive

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/mgnlia/lx-agent/internal/canvas"
	"github.com/mgnlia/lx-agent/internal/htmldoc"
)

// GradesFile is the per-course report of the student's own results.
const GradesFile = "성적.html"

// renderGrades builds a course report: every assignment with its score, the
// student's submission, and any instructor feedback.
//
// Scores and feedback comments live only in the LMS and disappear with the
// enrollment, so they are archived as a document rather than left as API data.
func renderGrades(
	course canvas.Course,
	grades *canvas.Grades,
	subs []canvas.Submission,
	resolve htmldoc.LinkResolver,
) []byte {
	b := htmldoc.Open(course.Name+" 성적", "제출물 · 점수 · 피드백 기록")

	var head [][2]string
	if grades != nil {
		head = append(head,
			[2]string{"현재 점수", scoreText(grades.CurrentScore, grades.CurrentGrade)},
			[2]string{"최종 점수", scoreText(grades.FinalScore, grades.FinalGrade)},
		)
	}
	head = append(head, [2]string{"제출 항목", fmt.Sprintf("%d개", len(subs))})
	htmldoc.Table(b, head)

	sort.Slice(subs, func(i, j int) bool {
		return assignmentName(subs[i]) < assignmentName(subs[j])
	})

	htmldoc.Heading(b, "과제별 결과")
	b.WriteString("<table><tr><th>과제</th><td class=\"num\">점수</td><td>제출</td></tr>")
	for _, s := range subs {
		a := s.Assignment
		points := ""
		if a != nil && a.PointsPossible > 0 {
			points = fmt.Sprintf(" / %g", a.PointsPossible)
		}
		score := "-"
		if s.Score != nil {
			score = fmt.Sprintf("%g%s", *s.Score, points)
		} else if s.Grade != "" {
			score = s.Grade
		}

		fmt.Fprintf(b, `<tr><th>%s</th><td class="num">%s</td><td>%s</td></tr>`,
			htmldoc.Esc(assignmentName(s)), htmldoc.Esc(score), htmldoc.Esc(submittedText(s)))
	}
	b.WriteString("</table>")

	// Feedback is the least replaceable part of a course, so it gets its own
	// section rather than being folded into a table cell.
	var fb []canvas.Submission
	for _, s := range subs {
		if len(s.Comments) > 0 {
			fb = append(fb, s)
		}
	}
	if len(fb) > 0 {
		htmldoc.Heading(b, "피드백")
		for _, s := range fb {
			for _, c := range s.Comments {
				who := c.AuthorName
				if who == "" {
					who = "채점자"
				}
				htmldoc.Section(b, fmt.Sprintf("%s — %s", assignmentName(s), who), c.Comment)
			}
		}
	}

	// Text and URL submissions have no file to download; without this they
	// would be archived as nothing at all.
	var inline []canvas.Submission
	for _, s := range subs {
		if strings.TrimSpace(s.Body) != "" || strings.TrimSpace(s.URL) != "" {
			inline = append(inline, s)
		}
	}
	if len(inline) > 0 {
		htmldoc.Heading(b, "제출 내용 (텍스트/링크)")
		for _, s := range inline {
			body := strings.TrimSpace(s.Body)
			if u := strings.TrimSpace(s.URL); u != "" {
				body = strings.TrimSpace(u + "\n\n" + body)
			}
			htmldoc.Section(b, assignmentName(s), body)
		}
	}

	// Assignment briefs are course-authored HTML. Escaping them printed raw
	// <a href=...> markup at the reader and hid that the linked file is already
	// archived; they are sanitized instead, with Canvas file links repointed at
	// the local copy.
	htmldoc.Heading(b, "과제 안내")
	for _, s := range subs {
		if s.Assignment == nil {
			continue
		}
		htmldoc.RichSection(b, assignmentName(s), s.Assignment.Description, resolve)
	}

	return htmldoc.Close(b, fmt.Sprintf("myetl.snu.ac.kr · %s 기준", time.Now().Format("2006-01-02")))
}

func assignmentName(s canvas.Submission) string {
	if s.Assignment != nil && strings.TrimSpace(s.Assignment.Name) != "" {
		return s.Assignment.Name
	}
	return fmt.Sprintf("assignment %d", s.AssignmentID)
}

func submittedText(s canvas.Submission) string {
	switch {
	case s.Excused:
		return "면제"
	case s.SubmittedAt != nil:
		out := s.SubmittedAt.Local().Format("2006-01-02")
		if s.Late {
			out += " (지각)"
		}
		return out
	case s.Missing:
		return "미제출"
	default:
		return "-"
	}
}

func scoreText(score *float64, grade string) string {
	switch {
	case score != nil && grade != "":
		return fmt.Sprintf("%g (%s)", *score, grade)
	case score != nil:
		return fmt.Sprintf("%g", *score)
	default:
		return grade
	}
}
