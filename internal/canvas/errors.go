package canvas

import (
	"errors"
	"fmt"
	"strings"
)

// APIError carries the HTTP status of a failed Canvas API call so callers can
// distinguish "your session died" from "this course hides its Files tab".
type APIError struct {
	StatusCode int
	URL        string
	Body       string
}

func (e *APIError) Error() string {
	body := strings.TrimSpace(e.Body)
	if len(body) > 300 {
		body = body[:300] + "..."
	}
	return fmt.Sprintf("API error %d: %s", e.StatusCode, body)
}

// IsAuthExpired reports whether the credential itself is no longer valid.
//
// Status alone cannot decide this. Observed live on myETL: a course the student
// may not read answers /students/submissions with 401 and a body of
// {"status":"권한이 없음"} — a permission fact about one course, not a dead
// session. Treating every 401 as expiry aborted the whole run over a single
// inaccessible course, so expiry requires Canvas's explicit "unauthenticated"
// marker.
//
// A genuinely dead credential is still caught: the course listing fails first,
// and a session that dies mid-run makes every source fail for a course, which
// the caller reports as a failure rather than as an empty course.
func IsAuthExpired(err error) bool {
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	if apiErr.StatusCode != 401 && apiErr.StatusCode != 403 {
		return false
	}
	return strings.Contains(strings.ToLower(apiErr.Body), "unauthenticated")
}

// IsForbidden reports whether the resource exists but is not readable by this
// user — typically a course with the Files tab disabled. Callers should fall
// back to another enumeration source rather than abort.
func IsForbidden(err error) bool {
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	return apiErr.StatusCode == 401 || apiErr.StatusCode == 403
}

// IsNotFound reports a 404.
func IsNotFound(err error) bool {
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	return apiErr.StatusCode == 404
}
