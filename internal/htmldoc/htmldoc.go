// Package htmldoc builds the small standalone HTML documents the archive
// generates (강의계획서, 성적). One shell here keeps them from drifting apart.
package htmldoc

import (
	"bytes"
	"fmt"
	"html"
	"regexp"
	"strings"
)

const style = `:root{--bg:#fbfaf8;--panel:#fff;--ink:#1b1a17;--muted:#6b675f;--line:#e5e1d9;--accent:#8a5a2b}
@media(prefers-color-scheme:dark){:root{--bg:#16151a;--panel:#1e1d23;--ink:#ece9e3;--muted:#9b968d;--line:#302e37;--accent:#d9a441}}
*{box-sizing:border-box}
body{margin:0;background:var(--bg);color:var(--ink);font:15px/1.6 -apple-system,BlinkMacSystemFont,"Apple SD Gothic Neo","Noto Sans KR",system-ui,sans-serif}
.wrap{max-width:820px;margin:0 auto;padding:24px 16px 64px}
h1{font-size:22px;margin:0 0 2px;letter-spacing:-.01em}
.sub{color:var(--muted);font-size:14px;margin-bottom:22px}
h2{font-size:13px;text-transform:uppercase;letter-spacing:.08em;color:var(--muted);margin:30px 0 10px}
table{width:100%;border-collapse:collapse;background:var(--panel);border:1px solid var(--line);border-radius:10px;overflow:hidden}
th,td{text-align:left;padding:9px 12px;border-bottom:1px solid var(--line);vertical-align:top}
tr:last-child th,tr:last-child td{border-bottom:0}
th{width:30%;color:var(--muted);font-weight:600;white-space:nowrap}
td.wk{width:64px;color:var(--muted);white-space:nowrap}
td.num{text-align:right;font-variant-numeric:tabular-nums;white-space:nowrap}
p.body{white-space:pre-wrap;background:var(--panel);border:1px solid var(--line);border-radius:10px;padding:12px 14px;margin:0}
.files a{display:block;padding:9px 12px;background:var(--panel);border:1px solid var(--line);border-radius:10px;margin-bottom:6px;color:var(--accent);text-decoration:none}
.note{color:var(--muted);font-size:13px;margin-top:6px}
.rich{background:var(--panel);border:1px solid var(--line);border-radius:10px;padding:4px 14px}
.rich a{color:var(--accent)}
.rich img{max-width:100%}
.rich pre{overflow-x:auto}
.rich table{margin:10px 0}
footer{margin-top:34px;color:var(--muted);font-size:12px}`

// Open starts a document and writes the header.
func Open(title, subtitle string) *bytes.Buffer {
	var b bytes.Buffer
	fmt.Fprintf(&b, `<!doctype html>
<html lang="ko"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="color-scheme" content="light dark">
<title>%s</title>
<style>%s</style></head><body><div class="wrap">
<h1>%s</h1>
`, Esc(title), style, Esc(title))
	if strings.TrimSpace(subtitle) != "" {
		fmt.Fprintf(&b, `<div class="sub">%s</div>`, Esc(subtitle))
	}
	return &b
}

// Close finishes the document with a footer note.
func Close(b *bytes.Buffer, footer string) []byte {
	if strings.TrimSpace(footer) != "" {
		fmt.Fprintf(b, `<footer>%s</footer>`, Esc(footer))
	}
	b.WriteString(`</div></body></html>`)
	return b.Bytes()
}

// Table writes a two-column table, skipping rows whose value is blank so an
// empty field never renders as a blank row.
func Table(b *bytes.Buffer, rows [][2]string) {
	var kept [][2]string
	for _, r := range rows {
		if strings.TrimSpace(r[1]) != "" {
			kept = append(kept, r)
		}
	}
	if len(kept) == 0 {
		return
	}
	b.WriteString("<table>")
	for _, r := range kept {
		fmt.Fprintf(b, "<tr><th>%s</th><td>%s</td></tr>", Esc(r[0]), Esc(r[1]))
	}
	b.WriteString("</table>")
}

// Section writes a heading plus a free-text block, or nothing when empty.
func Section(b *bytes.Buffer, heading, body string) {
	if strings.TrimSpace(body) == "" {
		return
	}
	fmt.Fprintf(b, `<h2>%s</h2><p class="body">%s</p>`, Esc(heading), EscBody(body))
}

// RichSection writes a heading plus course-authored HTML, sanitized.
func RichSection(b *bytes.Buffer, heading, rawHTML string, resolve LinkResolver) {
	body := Sanitize(rawHTML, resolve)
	if strings.TrimSpace(body) == "" {
		return
	}
	fmt.Fprintf(b, `<h2>%s</h2><div class="rich">%s</div>`, Esc(heading), body)
}

// Heading writes a section heading.
func Heading(b *bytes.Buffer, heading string) {
	fmt.Fprintf(b, "<h2>%s</h2>", Esc(heading))
}

func Esc(s string) string { return html.EscapeString(s) }

var brRe = regexp.MustCompile(`(?i)<br\s*/?>`)

// EscBody escapes free text that arrives with <br> tags embedded. Escaping
// those wholesale would print "<br>" to the reader; blocks render with
// white-space:pre-wrap, so a newline is the faithful substitute.
func EscBody(s string) string {
	return html.EscapeString(brRe.ReplaceAllString(s, "\n"))
}
