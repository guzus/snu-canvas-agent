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
// Canvas answers 401 (and an "unauthenticated" body) once a session cookie or
// token expires; a bare 403 usually means the resource is disabled for this
// user, not that auth is dead.
func IsAuthExpired(err error) bool {
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	if apiErr.StatusCode == 401 {
		return true
	}
	return apiErr.StatusCode == 403 && strings.Contains(strings.ToLower(apiErr.Body), "unauthenticated")
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
