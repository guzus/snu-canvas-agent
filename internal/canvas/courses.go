package canvas

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
)

func (c *Client) GetCourses(ctx context.Context) ([]Course, error) {
	params := url.Values{
		"enrollment_state": {"active"},
		"per_page":         {"100"},
	}

	raw, err := c.getPaginated(ctx, "/courses", params)
	if err != nil {
		return nil, err
	}

	var courses []Course
	for _, r := range raw {
		var course Course
		if err := json.Unmarshal(r, &course); err != nil {
			continue
		}
		courses = append(courses, course)
	}
	return courses, nil
}

func (c *Client) GetCourse(ctx context.Context, courseID int) (*Course, error) {
	var course Course
	err := c.getAll(ctx, fmt.Sprintf("/courses/%d", courseID), nil, &course)
	return &course, err
}

// Tab is a course navigation tab. External tabs are LTI tools (LearningX
// 주차학습, 강의/출결), whose content is not exposed through the Canvas API.
type Tab struct {
	ID      string `json:"id"`
	Label   string `json:"label"`
	Type    string `json:"type"`
	HTMLURL string `json:"html_url"`
}

// GetTabs lists a course's navigation tabs. Used to explain an empty course:
// SNU courses with the Files tab removed answer /files with 200 and an empty
// array rather than 403, so nothing else distinguishes "no materials" from
// "materials live behind an LTI tool this API cannot see".
func (c *Client) GetTabs(ctx context.Context, courseID int) ([]Tab, error) {
	params := url.Values{"per_page": {"50"}}

	raw, err := c.getPaginated(ctx, fmt.Sprintf("/courses/%d/tabs", courseID), params)
	if err != nil {
		return nil, err
	}

	var tabs []Tab
	for _, r := range raw {
		var t Tab
		if err := json.Unmarshal(r, &t); err != nil {
			continue
		}
		tabs = append(tabs, t)
	}
	return tabs, nil
}

// GetSyllabusBody returns the course's syllabus HTML. On SNU myETL this is not
// the syllabus itself but a wrapper around an iframe pointing at
// sugang.snu.ac.kr, which is where the real 강의계획서 lives.
func (c *Client) GetSyllabusBody(ctx context.Context, courseID int) (string, error) {
	params := url.Values{"include[]": {"syllabus_body"}}

	var course struct {
		SyllabusBody string `json:"syllabus_body"`
	}
	if err := c.getAll(ctx, fmt.Sprintf("/courses/%d", courseID), params, &course); err != nil {
		return "", err
	}
	return course.SyllabusBody, nil
}
