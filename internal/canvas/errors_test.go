package canvas

import "testing"

func TestIsAuthExpiredRequiresTheUnauthenticatedMarker(t *testing.T) {
	cases := []struct {
		name    string
		err     error
		expired bool
	}{
		{
			name:    "dead session",
			err:     &APIError{StatusCode: 401, Body: `{"status":"unauthenticated"}`},
			expired: true,
		},
		{
			// Observed live: a course the student cannot read answers 401 with
			// a localized permission message. Treating this as expiry aborted
			// the entire run over one inaccessible course.
			name:    "per-course permission denial, localized",
			err:     &APIError{StatusCode: 401, Body: `{"status":"권한이 없음","errors":[{"message":"사용자에게 이 동작을 수행할 권한이 없습니다"}]}`},
			expired: false,
		},
		{
			name:    "files tab disabled",
			err:     &APIError{StatusCode: 403, Body: `{"status":"unauthorized"}`},
			expired: false,
		},
		{
			name:    "not found",
			err:     &APIError{StatusCode: 404, Body: `{"errors":[{"message":"does not exist"}]}`},
			expired: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsAuthExpired(tc.err); got != tc.expired {
				t.Fatalf("IsAuthExpired = %v, want %v", got, tc.expired)
			}
			// Either way these remain "cannot read this", so callers can fall
			// back to another source.
			if tc.err.(*APIError).StatusCode == 401 || tc.err.(*APIError).StatusCode == 403 {
				if !IsForbidden(tc.err) {
					t.Error("IsForbidden should hold for 401/403")
				}
			}
		})
	}
}
