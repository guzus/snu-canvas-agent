package htmldoc

import (
	"strconv"
	"strings"
	"testing"
)

func resolver(ids ...int) LinkResolver {
	set := map[int]bool{}
	for _, id := range ids {
		set[id] = true
	}
	return func(id int) (string, bool) {
		if !set[id] {
			return "", false
		}
		return "/file/" + strconv.Itoa(id), true
	}
}

func TestSanitizeKeepsReadableMarkup(t *testing.T) {
	in := `<p>Read <strong>chapter 3</strong> then:</p><ul><li>part a</li><li>part b</li></ul>
<pre><code>make test</code></pre><h3>Notes</h3><p>line<br>break</p>`

	out := Sanitize(in, nil)
	for _, want := range []string{"<p>", "<strong>chapter 3</strong>", "<ul>", "<li>part a</li>",
		"<pre>", "<code>make test</code>", "<h3>Notes</h3>", "<br>"} {
		if !strings.Contains(out, want) {
			t.Errorf("lost %q from %q", want, out)
		}
	}
}

// The briefs are third-party HTML rendered into a page served from the archive.
func TestSanitizeStripsActiveContent(t *testing.T) {
	cases := map[string]string{
		"script":       `<p>ok</p><script>alert(1)</script>`,
		"style":        `<style>body{display:none}</style><p>ok</p>`,
		"iframe":       `<iframe src="https://evil.example"></iframe><p>ok</p>`,
		"event hander": `<p onclick="alert(1)">ok</p>`,
		"inline style": `<p style="position:fixed;top:0">ok</p>`,
		"javascript:":  `<a href="javascript:alert(1)">ok</a>`,
		"data uri":     `<a href="data:text/html,<script>alert(1)</script>">ok</a>`,
		"form":         `<form action="https://evil.example"><input name="x"></form><p>ok</p>`,
		"svg":          `<svg onload="alert(1)"></svg><p>ok</p>`,
	}

	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			out := Sanitize(in, nil)
			for _, bad := range []string{"<script", "<style", "<iframe", "<form", "<input",
				"<svg", "onclick", "onload", "style=", "javascript:", "data:text/html"} {
				if strings.Contains(strings.ToLower(out), bad) {
					t.Fatalf("%q survived: %q", bad, out)
				}
			}
			if !strings.Contains(out, "ok") {
				t.Errorf("legitimate text was dropped: %q", out)
			}
		})
	}
}

// Unknown tags are unwrapped rather than deleted, so no text is lost to an
// element this allowlist has not heard of.
func TestSanitizeUnwrapsUnknownTagsKeepingText(t *testing.T) {
	out := Sanitize(`<section><custom-el>keep this</custom-el></section>`, nil)
	if !strings.Contains(out, "keep this") {
		t.Fatalf("text lost: %q", out)
	}
	if strings.Contains(out, "custom-el") || strings.Contains(out, "<section") {
		t.Fatalf("unknown tag survived: %q", out)
	}
}

func TestSanitizeRepointsArchivedFileLinks(t *testing.T) {
	in := `<a class="instructure_file_link" title="hw2.zip"
	         href="https://myetl.snu.ac.kr/courses/296215/files/8219881/download?wrap=1"
	         target="_blank" data-api-returntype="File">hw2.zip</a>`

	out := Sanitize(in, resolver(8219881))
	if !strings.Contains(out, `href="/file/8219881"`) {
		t.Fatalf("link not repointed: %q", out)
	}
	if !strings.Contains(out, `title="hw2.zip"`) {
		t.Error("title dropped")
	}
	// Canvas bookkeeping attributes carry nothing a reader needs.
	for _, bad := range []string{"data-api", "instructure_file_link", "target="} {
		if strings.Contains(out, bad) {
			t.Errorf("%q survived: %q", bad, out)
		}
	}
	if !strings.Contains(out, `rel="noopener noreferrer"`) {
		t.Error("rel not added")
	}
}

func TestSanitizeKeepsUnarchivedLinksIntact(t *testing.T) {
	in := `<a href="https://myetl.snu.ac.kr/courses/1/files/424242/download">x</a>` +
		`<a href="https://example.com/spec">spec</a>`

	out := Sanitize(in, resolver(999))
	if !strings.Contains(out, "files/424242/download") {
		t.Error("unarchived Canvas link should be preserved for a live session")
	}
	if !strings.Contains(out, "https://example.com/spec") {
		t.Error("external link dropped")
	}
}

func TestSanitizeEscapesText(t *testing.T) {
	out := Sanitize(`<p>a &lt; b &amp;&amp; c > d</p>`, nil)
	if strings.Contains(out, "a < b") {
		t.Fatalf("text not re-escaped: %q", out)
	}
	if !strings.Contains(out, "&amp;") || !strings.Contains(out, "&lt;") {
		t.Fatalf("entities lost: %q", out)
	}
}

func TestSanitizeEmptyInput(t *testing.T) {
	for _, in := range []string{"", "   ", "\n"} {
		if got := Sanitize(in, nil); got != "" {
			t.Errorf("Sanitize(%q) = %q", in, got)
		}
	}
}
