package archive

import (
	"fmt"
	"path"
	"strings"
	"unicode"
)

// sanitizeSegment turns an LMS-supplied name into one safe path segment.
// Canvas display names routinely contain "/" (e.g. "1주차 09/01"), and macOS
// renders ":" in a POSIX name as "/" in Finder, so both have to go.
func sanitizeSegment(name string) string {
	name = strings.TrimSpace(name)

	var b strings.Builder
	for _, r := range name {
		switch {
		case r == '/' || r == '\\' || r == ':':
			b.WriteRune('-')
		case r == 0:
			// drop
		case unicode.IsControl(r):
			b.WriteRune(' ')
		default:
			b.WriteRune(r)
		}
	}

	out := strings.Join(strings.Fields(b.String()), " ")
	out = strings.Trim(out, " .")
	if out == "." || out == ".." {
		return ""
	}
	return truncateName(out, 150)
}

// truncateName shortens a name to max bytes while preserving its extension,
// so a pathological display name cannot blow past the filesystem limit.
func truncateName(name string, max int) string {
	if len(name) <= max {
		return name
	}
	ext := path.Ext(name)
	if len(ext) > 20 {
		ext = ""
	}
	stem := name[:len(name)-len(ext)]
	for len(stem) > 0 && len(stem)+len(ext) > max {
		_, size := lastRune(stem)
		stem = stem[:len(stem)-size]
	}
	return strings.TrimRight(stem, " .") + ext
}

func lastRune(s string) (rune, int) {
	r := []rune(s)
	if len(r) == 0 {
		return 0, 0
	}
	last := r[len(r)-1]
	return last, len(string(last))
}

// pathKey is the identity a path has *on disk*. macOS volumes are
// case-insensitive by default, so "Lecture.pdf" and "lecture.pdf" are one file
// there — comparing raw strings would let two Canvas files overwrite each other
// and then re-download in alternation on every scheduled run.
func pathKey(rel string) string {
	return strings.ToLower(rel)
}

// disambiguate appends the Canvas file ID before the extension when the
// intended path is already claimed by a different file. taken maps pathKey to
// the file ID holding it; the suffixed name is re-checked, because sanitizing
// can make a suffixed name collide in turn (an "x:" and an "x--3" in one
// folder both reduce to "x--3").
func disambiguate(rel string, fileID int, taken map[string]int) string {
	ext := path.Ext(rel)
	stem := strings.TrimSuffix(rel, ext)

	candidate := fmt.Sprintf("%s-%d%s", stem, fileID, ext)
	for n := 2; ; n++ {
		owner, ok := taken[pathKey(candidate)]
		if !ok || owner == fileID {
			return candidate
		}
		candidate = fmt.Sprintf("%s-%d-%d%s", stem, fileID, n, ext)
	}
}

// folderRelPath converts a Canvas folder full_name ("course files/2주차") into
// a relative directory, dropping the synthetic root segment Canvas prepends.
func folderRelPath(fullName string) string {
	fullName = strings.Trim(strings.TrimSpace(fullName), "/")
	if fullName == "" {
		return ""
	}
	parts := strings.Split(fullName, "/")
	if len(parts) > 0 && (parts[0] == "course files" || parts[0] == "course_files") {
		parts = parts[1:]
	}

	var clean []string
	for _, p := range parts {
		if s := sanitizeSegment(p); s != "" {
			clean = append(clean, s)
		}
	}
	return path.Join(clean...)
}
