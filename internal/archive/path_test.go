package archive

import "testing"

func TestSanitizeSegment(t *testing.T) {
	cases := map[string]string{
		"1주차 09/01 개요.pdf": "1주차 09-01 개요.pdf",
		` back\slash.pdf `: "back-slash.pdf",
		"a:b.txt":          "a-b.txt",
		"..":               "",
		"trailing dots...": "trailing dots",
	}
	for in, want := range cases {
		if got := sanitizeSegment(in); got != want {
			t.Errorf("sanitizeSegment(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFolderRelPathDropsCanvasRoot(t *testing.T) {
	cases := map[string]string{
		"course files":        "",
		"course files/1주차":    "1주차",
		"course files/1주차/보충": "1주차/보충",
		"/course files/a:b/":  "a-b",
	}
	for in, want := range cases {
		if got := folderRelPath(in); got != want {
			t.Errorf("folderRelPath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTruncateNamePreservesExtension(t *testing.T) {
	long := ""
	for i := 0; i < 100; i++ {
		long += "한"
	}
	got := truncateName(long+".pdf", 150)
	if len(got) > 150 {
		t.Fatalf("len = %d, want <= 150", len(got))
	}
	if got[len(got)-4:] != ".pdf" {
		t.Fatalf("extension lost: %q", got[len(got)-10:])
	}
}

func TestDisambiguate(t *testing.T) {
	if got := disambiguate("course/week/a.pdf", 42, map[string]int{}); got != "course/week/a-42.pdf" {
		t.Fatalf("got %q", got)
	}

	// Sanitizing can make the suffixed name collide in turn: "x:" and "x--3"
	// in one folder both reduce to "x--3".
	taken := map[string]int{"w/x--3.pdf": 1}
	if got := disambiguate("w/x-.pdf", 3, taken); got != "w/x--3-2.pdf" {
		t.Fatalf("suffixed collision not resolved: %q", got)
	}

	// Collisions must be detected case-insensitively, as on macOS.
	taken = map[string]int{"w/x-9.pdf": 1}
	if got := disambiguate("w/X.pdf", 9, taken); got != "w/X-9-2.pdf" {
		t.Fatalf("case-insensitive collision not resolved: %q", got)
	}
}
