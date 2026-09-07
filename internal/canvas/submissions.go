package canvas

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
)

// GetSelfSubmissions returns the signed-in student's own submissions for a
// course, including their uploaded files, scores and instructor comments.
//
// This is the part of a course that exists nowhere else: lecture slides can be
// asked for again, a graded submission and its feedback cannot.
func (c *Client) GetSelfSubmissions(ctx context.Context, courseID int) ([]Submission, error) {
	params := url.Values{
		"student_ids[]": {"self"},
		"include[]":     {"submission_comments", "assignment"},
		"per_page":      {"100"},
	}

	raw, err := c.getPaginated(ctx, fmt.Sprintf("/courses/%d/students/submissions", courseID), params)
	if err != nil {
		return nil, err
	}

	var subs []Submission
	for _, r := range raw {
		var s Submission
		if err := json.Unmarshal(r, &s); err != nil {
			continue
		}
		s.CourseID = courseID
		subs = append(subs, s)
	}
	return subs, nil
}

// GetSelfGrades returns the student's own enrollment grades for a course.
func (c *Client) GetSelfGrades(ctx context.Context, courseID int) (*Grades, error) {
	params := url.Values{"user_id": {"self"}}

	raw, err := c.getPaginated(ctx, fmt.Sprintf("/courses/%d/enrollments", courseID), params)
	if err != nil {
		return nil, err
	}
	for _, r := range raw {
		var e struct {
			Grades *Grades `json:"grades"`
		}
		if err := json.Unmarshal(r, &e); err != nil || e.Grades == nil {
			continue
		}
		return e.Grades, nil
	}
	return nil, nil
}
