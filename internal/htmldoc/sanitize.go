package htmldoc

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// allowedTags is the subset of markup worth keeping in an archived document.
// Anything else is unwrapped (children kept, tag dropped) rather than deleted,
// so no text is lost to an unfamiliar element.
var allowedTags = map[atom.Atom]bool{
	atom.P: true, atom.Br: true, atom.A: true, atom.Ul: true, atom.Ol: true,
	atom.Li: true, atom.Strong: true, atom.B: true, atom.Em: true, atom.I: true,
	atom.U: true, atom.Code: true, atom.Pre: true, atom.Blockquote: true,
	atom.H1: true, atom.H2: true, atom.H3: true, atom.H4: true, atom.H5: true,
	atom.H6: true, atom.Table: true, atom.Thead: true, atom.Tbody: true,
	atom.Tr: true, atom.Td: true, atom.Th: true, atom.Hr: true, atom.Img: true,
}

// droppedTags are removed with their contents. Keeping a <script> body as text
// would dump code into the page; keeping <style> would let course CSS restyle
// the archive.
var droppedTags = map[atom.Atom]bool{
	atom.Script: true, atom.Style: true, atom.Iframe: true, atom.Object: true,
	atom.Embed: true, atom.Form: true, atom.Input: true, atom.Button: true,
	atom.Link: true, atom.Meta: true, atom.Noscript: true, atom.Svg: true,
}

// canvasFileRe matches the file id in a Canvas file URL.
var canvasFileRe = regexp.MustCompile(`/files/(\d+)`)

// LinkResolver reports where an archived Canvas file can be read, given its id.
type LinkResolver func(fileID int) (href string, ok bool)

// Sanitize renders course-authored HTML (assignment briefs, announcements) as
// safe markup, and repoints Canvas file links at the archived copy.
//
// Escaping this content wholesale — the previous behaviour — printed raw
// <a href=...> markup at the reader, which is both unreadable and hides the
// fact that the linked file is already in the archive. Rendering it verbatim is
// not an option either: it is third-party HTML that would otherwise carry
// scripts and off-host requests into a page served from the archive.
//
// Links to files that were not archived keep their original URL, so a live
// Canvas session can still follow them.
func Sanitize(raw string, resolve LinkResolver) string {
	if strings.TrimSpace(raw) == "" {
		return ""
	}

	nodes, err := html.ParseFragment(strings.NewReader(raw), &html.Node{
		Type: html.ElementNode, Data: "div", DataAtom: atom.Div,
	})
	if err != nil {
		// Fall back to escaped text rather than emitting unchecked markup.
		return EscBody(raw)
	}

	var b strings.Builder
	for _, n := range nodes {
		writeNode(&b, n, resolve)
	}
	return strings.TrimSpace(b.String())
}

func writeNode(b *strings.Builder, n *html.Node, resolve LinkResolver) {
	switch n.Type {
	case html.TextNode:
		b.WriteString(Esc(n.Data))
		return
	case html.CommentNode, html.DoctypeNode:
		return
	case html.DocumentNode:
		writeChildren(b, n, resolve)
		return
	case html.ElementNode:
		// fall through
	default:
		return
	}

	if droppedTags[n.DataAtom] {
		return
	}
	if !allowedTags[n.DataAtom] {
		writeChildren(b, n, resolve) // unwrap: keep the text, drop the tag
		return
	}

	tag := n.Data
	attrs := safeAttrs(n, resolve)

	b.WriteString("<" + tag)
	for _, a := range attrs {
		fmt.Fprintf(b, ` %s="%s"`, a[0], Esc(a[1]))
	}

	if n.DataAtom == atom.Br || n.DataAtom == atom.Hr || n.DataAtom == atom.Img {
		b.WriteString(">")
		return
	}
	b.WriteString(">")
	writeChildren(b, n, resolve)
	b.WriteString("</" + tag + ">")
}

func writeChildren(b *strings.Builder, n *html.Node, resolve LinkResolver) {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		writeNode(b, c, resolve)
	}
}

// safeAttrs keeps only href/src/title/alt, and only after rewriting or
// vetting the URL. Every other attribute is dropped, which removes event
// handlers and inline styles without needing to enumerate them.
func safeAttrs(n *html.Node, resolve LinkResolver) [][2]string {
	var out [][2]string

	for _, a := range n.Attr {
		switch strings.ToLower(a.Key) {
		case "title", "alt":
			if a.Val != "" {
				out = append(out, [2]string{strings.ToLower(a.Key), a.Val})
			}
		case "href":
			if n.DataAtom != atom.A {
				continue
			}
			if u, ok := rewriteURL(a.Val, resolve); ok {
				out = append(out, [2]string{"href", u})
			}
		case "src":
			if n.DataAtom != atom.Img {
				continue
			}
			if u, ok := rewriteURL(a.Val, resolve); ok {
				out = append(out, [2]string{"src", u})
			}
		}
	}

	if n.DataAtom == atom.A {
		out = append(out, [2]string{"rel", "noopener noreferrer"})
	}
	return out
}

// rewriteURL points a Canvas file URL at the archived copy when there is one,
// and otherwise passes through http(s) links only — dropping javascript:,
// data: and anything else that could execute.
func rewriteURL(raw string, resolve LinkResolver) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", false
	}

	if resolve != nil {
		if m := canvasFileRe.FindStringSubmatch(raw); len(m) > 1 {
			if id, err := strconv.Atoi(m[1]); err == nil {
				if href, ok := resolve(id); ok {
					return href, true
				}
			}
		}
	}

	lower := strings.ToLower(raw)
	if strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") ||
		strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, "#") {
		return raw, true
	}
	return "", false
}
