package archive

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/mgnlia/lx-agent/internal/canvas"
	"github.com/mgnlia/lx-agent/internal/htmldoc"
	"golang.org/x/net/html"
)

// HomepageDir is the per-course folder holding a mirrored course website.
const HomepageDir = "과목홈페이지"

const (
	homepageUserAgent = "Mozilla/5.0 (compatible; lx-agent)"
	maxHomepagePages  = 8
	maxHomepageFiles  = 80
	maxHomepageBytes  = 64 << 20 // 64 MiB per file; lecture slides are a few MB
)

var errHomepageTooBig = errors.New("homepage file exceeds max-mb")

// homepageLabelRe finds an explicitly labeled course website in syllabus remarks.
// Random URLs in the same block (Google Forms, department portals) must not be
// treated as something to crawl.
var homepageLabelRe = regexp.MustCompile(`(?i)(?:과목\s*)?(?:강의\s*)?(?:홈페이지|homepage|course\s*(?:web\s*)?site|course\s*page)\s*[:：]?\s*(https?://[^\s<>"'）)]+)`)

var docExt = map[string]bool{
	".pdf": true, ".ppt": true, ".pptx": true, ".doc": true, ".docx": true,
	".hwp": true, ".hwpx": true, ".zip": true, ".tar": true, ".gz": true,
	".tgz": true, ".rar": true, ".7z": true, ".txt": true, ".md": true,
	".csv": true, ".xls": true, ".xlsx": true, ".pages": true, ".key": true,
}

var assetExt = map[string]bool{
	".css": true, ".js": true, ".mjs": true, ".map": true,
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".svg": true,
	".ico": true, ".webp": true, ".woff": true, ".woff2": true, ".ttf": true,
	".eot": true, ".json": true, ".xml": true, ".webmanifest": true,
}

var skipExternalHost = map[string]bool{
	"cdnjs.cloudflare.com": true,
	"cdn.jsdelivr.net":     true,
	"unpkg.com":            true,
	"fonts.googleapis.com": true,
	"fonts.gstatic.com":    true,
	"use.fontawesome.com":  true,
	"www.youtube.com":      true,
	"youtube.com":          true,
	"youtu.be":             true,
	"sourcethemes.com":     true,
	"www.sourcethemes.com": true,
	"gohugo.io":            true,
}

type hpFile struct {
	URL  string `json:"url"`
	Name string `json:"name"`
	Size int64  `json:"size"`
}

type hpLink struct {
	URL  string `json:"url"`
	Text string `json:"text,omitempty"`
}

type hpNote struct {
	URL      string   `json:"url"`
	Files    []hpFile `json:"files"`
	Pages    []string `json:"pages,omitempty"`
	External []hpLink `json:"external,omitempty"`
	Error    string   `json:"error,omitempty"`
}

type hpDiscover struct {
	files    []hpFile
	pages    []string
	external []hpLink
	html     []byte
}

// parseHomepageURLs returns course-website URLs labeled as such in syllabus
// remarks. Unlabeled URLs are ignored on purpose: a remarks block also carries
// Google Forms and department links, and crawling those would not be "the
// course homepage".
func parseHomepageURLs(text string) []string {
	matches := homepageLabelRe.FindAllStringSubmatch(text, -1)
	if len(matches) == 0 {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, m := range matches {
		raw := trimFetchedURL(m[1])
		if raw == "" || seen[raw] {
			continue
		}
		if _, err := url.Parse(raw); err != nil {
			continue
		}
		seen[raw] = true
		out = append(out, raw)
	}
	return out
}

func trimFetchedURL(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimRight(s, ".,;:)]}>\"'")
	return s
}

// homepageEntryID is a stable negative id derived from course + resource URL.
//
// Canvas file ids are positive. Syllabus/grades ids are -(courseID*100+n),
// which for SNU's six-digit course ids sit around -3e7. Hashing into
// [-2e9, -1e9] keeps homepage artifacts in their own key space so a new
// slide cannot collide with a 강의계획서.html written on an earlier run.
// The course id is in the key so two Canvas courses that share a website
// do not overwrite each other's manifest entries.
func homepageEntryID(courseID int, rawURL string) int {
	sum := sha256.Sum256([]byte(fmt.Sprintf("lx-homepage\x00%d\x00%s", courseID, rawURL)))
	n := int(binary.BigEndian.Uint32(sum[:4]) & 0x3fffffff)
	return -(1_000_000_000 + n)
}

func homepageURLFor(courseID int, remarks string, opts Options) string {
	if opts.Homepages != nil {
		if u := strings.TrimSpace(opts.Homepages[courseID]); u != "" {
			return u
		}
	}
	urls := parseHomepageURLs(remarks)
	if len(urls) == 0 {
		return ""
	}
	return urls[0]
}

func validHomepage(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("homepage URL must be http(s)")
	}
	if u.Host == "" {
		return nil, fmt.Errorf("homepage URL has no host")
	}
	segs := strings.FieldsFunc(u.Path, func(r rune) bool { return r == '/' })
	// A lab root (https://csl.snu.ac.kr/) would pull every course the lab ever
	// taught. The syllabus line is the course-and-term path; refuse anything
	// shallower than two segments.
	if len(segs) < 2 {
		return nil, fmt.Errorf("homepage path %q is too shallow to crawl", u.Path)
	}
	return u, nil
}

func (s *Syncer) archiveHomepage(
	ctx context.Context,
	course canvas.Course,
	courseDir, remarks string,
	manifest *Manifest,
	opts Options,
	result *Result,
	cr *CourseResult,
) {
	raw := homepageURLFor(course.ID, remarks, opts)
	if raw == "" {
		return
	}
	home, err := validHomepage(raw)
	if err != nil {
		cr.Warnings = append(cr.Warnings, fmt.Sprintf("homepage skipped (%v)", err))
		return
	}

	client := &http.Client{
		Timeout:       2 * time.Minute,
		CheckRedirect: stayOnHomepage(home),
	}
	disc, discErr := discoverHomepage(ctx, client, home)

	base := path.Join(courseDir, HomepageDir)
	var saved []hpFile
	claimed := map[string]int{}

	if discErr == nil {
		if len(disc.html) > 0 {
			s.writeGenerated(course, path.Join(base, "index.html"), "index.html",
				disc.html, homepageEntryID(course.ID, home.String()+"\x00snapshot"),
				"homepage", manifest, opts, result, cr)
		}
		for _, f := range disc.files {
			fileURL, err := url.Parse(f.URL)
			if err != nil {
				continue
			}
			sub := relUnderHome(fileURL, home)
			if sub == "" {
				continue
			}
			rel := path.Join(base, sub)
			id := homepageEntryID(course.ID, f.URL)
			if owner, ok := claimed[pathKey(rel)]; ok && owner != id {
				rel = disambiguate(rel, id, claimed)
			}
			claimed[pathKey(rel)] = id
			name := path.Base(rel)
			if opts.DryRun {
				result.Downloaded++
				cr.Downloaded++
				result.New = append(result.New, FileResult{
					CourseName: course.Name, Display: name, RelPath: rel,
				})
				saved = append(saved, f)
				continue
			}
			n, err := s.fetchHomepageFile(ctx, client, course, opts.Dir, rel, name, f.URL, id, opts.MaxFileBytes, manifest, result, cr)
			if err != nil {
				continue
			}
			f.Size = n
			saved = append(saved, f)
		}
	}

	errMsg := ""
	if discErr != nil {
		errMsg = discErr.Error()
		cr.Warnings = append(cr.Warnings, fmt.Sprintf("homepage fetch failed (%v)", shortErr(discErr)))
	}

	note := hpNote{
		URL:      home.String(),
		Files:    saved,
		Pages:    disc.pages,
		External: disc.external,
		Error:    errMsg,
	}
	rawJSON, _ := json.MarshalIndent(note, "", "  ")
	s.writeGenerated(course, path.Join(base, "homepage.json"), "homepage.json",
		rawJSON, homepageEntryID(course.ID, home.String()+"\x00json"),
		"homepage", manifest, opts, result, cr)
	s.writeGenerated(course, path.Join(base, "homepage.html"), "homepage.html",
		renderHomepage(course, home.String(), saved, disc.external, errMsg, manifest),
		homepageEntryID(course.ID, home.String()+"\x00html"),
		"homepage", manifest, opts, result, cr)
}

func (s *Syncer) fetchHomepageFile(
	ctx context.Context,
	client *http.Client,
	course canvas.Course,
	dir, rel, name, rawURL string,
	id int,
	maxBytes int64,
	manifest *Manifest,
	result *Result,
	cr *CourseResult,
) (int64, error) {
	abs := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		s.recordFailure(result, cr, course.Name, name, err)
		return 0, err
	}

	tmp := abs + ".fetch"
	n, content, err := getURL(ctx, client, rawURL, tmp, maxHomepageBytes)
	if err != nil {
		os.Remove(tmp)
		s.recordFailure(result, cr, course.Name, name, err)
		return 0, err
	}
	if maxBytes > 0 && n > maxBytes {
		os.Remove(tmp)
		cr.Skipped++
		result.Skipped++
		result.SkippedTooBig++
		return 0, errHomepageTooBig
	}
	if strings.EqualFold(path.Ext(name), ".pdf") && !looksLikePDF(content) {
		os.Remove(tmp)
		err := fmt.Errorf("expected PDF, got %s", sniffContent(content))
		s.recordFailure(result, cr, course.Name, name, err)
		return 0, err
	}
	if sameContent(abs, content) {
		os.Remove(tmp)
		cr.Skipped++
		result.Skipped++
		manifest.Put(id, generatedEntry(course, rel, name, "homepage", n, content))
		return n, nil
	}
	if err := os.Rename(tmp, abs); err != nil {
		os.Remove(tmp)
		s.recordFailure(result, cr, course.Name, name, err)
		return 0, err
	}
	manifest.Put(id, generatedEntry(course, rel, name, "homepage", n, content))
	cr.Downloaded++
	result.Downloaded++
	result.Bytes += n
	result.New = append(result.New, FileResult{
		CourseName: course.Name, Display: name, RelPath: rel, Size: n,
	})
	s.logger.Info("archived homepage file", "course", course.Name, "file", rel, "bytes", n)
	return n, nil
}

func discoverHomepage(ctx context.Context, client *http.Client, home *url.URL) (hpDiscover, error) {
	out := hpDiscover{}
	seenPage := map[string]bool{}
	seenFile := map[string]bool{}
	seenExt := map[string]bool{}
	queue := []*url.URL{home}

	for len(queue) > 0 && len(seenPage) < maxHomepagePages {
		page := queue[0]
		queue = queue[1:]
		key := canonicalURL(page)
		if seenPage[key] {
			continue
		}
		seenPage[key] = true

		_, body, err := getURL(ctx, client, page.String(), "", maxHomepageBytes)
		if err != nil {
			if len(seenPage) == 1 {
				return out, err
			}
			continue
		}
		if len(out.html) == 0 {
			out.html = body
		}
		out.pages = append(out.pages, page.String())

		for _, link := range collectHrefs(body, page) {
			switch {
			case isDocURL(link.u):
				if !underPathPrefix(link.u, home) {
					addExternal(&out, seenExt, link)
					continue
				}
				if seenFile[canonicalURL(link.u)] {
					continue
				}
				if len(out.files) >= maxHomepageFiles {
					continue
				}
				seenFile[canonicalURL(link.u)] = true
				out.files = append(out.files, hpFile{
					URL:  link.u.String(),
					Name: docName(link.u),
				})
			case isAssetURL(link.u):
				continue
			case underPathPrefix(link.u, home) && isHTMLURL(link.u):
				if !seenPage[canonicalURL(link.u)] {
					queue = append(queue, link.u)
				}
			default:
				addExternal(&out, seenExt, link)
			}
		}
	}

	sort.Slice(out.files, func(i, j int) bool { return out.files[i].Name < out.files[j].Name })
	sort.Slice(out.external, func(i, j int) bool { return out.external[i].URL < out.external[j].URL })
	return out, nil
}

type hpHref struct {
	u    *url.URL
	text string
}

func collectHrefs(body []byte, base *url.URL) []hpHref {
	z := html.NewTokenizer(bytes.NewReader(body))
	var out []hpHref
	var inA bool
	var href string
	var text strings.Builder
	for {
		tt := z.Next()
		switch tt {
		case html.ErrorToken:
			return out
		case html.StartTagToken, html.SelfClosingTagToken:
			tok := z.Token()
			if tok.Data == "a" {
				inA = true
				href = ""
				text.Reset()
				for _, a := range tok.Attr {
					if strings.EqualFold(a.Key, "href") {
						href = a.Val
						break
					}
				}
			}
		case html.TextToken:
			if inA {
				text.Write(z.Text())
			}
		case html.EndTagToken:
			tok := z.Token()
			if tok.Data == "a" && inA {
				inA = false
				if href == "" || strings.HasPrefix(href, "javascript:") || strings.HasPrefix(href, "mailto:") {
					continue
				}
				ref, err := url.Parse(href)
				if err != nil {
					continue
				}
				u := base.ResolveReference(ref)
				u.Fragment = ""
				out = append(out, hpHref{u: u, text: strings.TrimSpace(text.String())})
			}
		}
	}
}

func addExternal(out *hpDiscover, seen map[string]bool, link hpHref) {
	if link.u.Scheme != "http" && link.u.Scheme != "https" {
		return
	}
	host := strings.ToLower(link.u.Hostname())
	if skipExternalHost[host] {
		return
	}
	key := canonicalURL(link.u)
	if seen[key] {
		return
	}
	seen[key] = true
	out.external = append(out.external, hpLink{URL: link.u.String(), Text: link.text})
}

func stayOnHomepage(home *url.URL) func(*http.Request, []*http.Request) error {
	return func(req *http.Request, via []*http.Request) error {
		if len(via) >= 8 {
			return fmt.Errorf("too many redirects")
		}
		if !underPathPrefix(req.URL, home) {
			return fmt.Errorf("redirect left course path")
		}
		return nil
	}
}

// homepageDir is the directory prefix we are willing to crawl. A homepage
// URL that names a file (…/index.html) is treated as that file's directory,
// otherwise relative slides.pdf would sit next to it and fail the prefix check.
func homepageDir(u *url.URL) string {
	p := strings.TrimSuffix(u.Path, "/")
	ext := strings.ToLower(path.Ext(p))
	if ext == ".html" || ext == ".htm" || docExt[ext] || assetExt[ext] {
		p = path.Dir(p)
	}
	return strings.TrimSuffix(p, "/")
}

func underPathPrefix(u, home *url.URL) bool {
	if u == nil || home == nil || !strings.EqualFold(u.Host, home.Host) {
		return false
	}
	prefix := homepageDir(home)
	if prefix == "" {
		return false
	}
	p := u.Path
	return p == prefix || strings.HasPrefix(p, prefix+"/")
}

func relUnderHome(u, home *url.URL) string {
	if u == nil {
		return ""
	}
	prefix := homepageDir(home)
	rest := u.Path
	if prefix != "" && (rest == prefix || strings.HasPrefix(rest, prefix+"/")) {
		rest = strings.TrimPrefix(rest, prefix)
	}
	rest = strings.TrimPrefix(rest, "/")
	if rest == "" {
		rest = docName(u)
	}
	var segs []string
	for _, p := range strings.Split(rest, "/") {
		if decoded, err := url.PathUnescape(p); err == nil {
			p = decoded
		}
		if s := sanitizeSegment(p); s != "" {
			segs = append(segs, s)
		}
	}
	return path.Join(segs...)
}

func canonicalURL(u *url.URL) string {
	c := *u
	c.Fragment = ""
	return c.String()
}

func isDocURL(u *url.URL) bool {
	return docExt[strings.ToLower(path.Ext(u.Path))]
}

func isAssetURL(u *url.URL) bool {
	return assetExt[strings.ToLower(path.Ext(u.Path))]
}

func isHTMLURL(u *url.URL) bool {
	ext := strings.ToLower(path.Ext(u.Path))
	return ext == "" || ext == ".html" || ext == ".htm"
}

func docName(u *url.URL) string {
	name := path.Base(u.Path)
	if name == "" || name == "." || name == "/" {
		return "download"
	}
	if decoded, err := url.PathUnescape(name); err == nil {
		name = decoded
	}
	return name
}

func looksLikePDF(b []byte) bool {
	return bytes.HasPrefix(bytes.TrimSpace(b), []byte("%PDF"))
}

func sniffContent(b []byte) string {
	head := strings.ToLower(strings.TrimSpace(string(b[:min(len(b), 64)])))
	switch {
	case looksLikePDF(b):
		return "PDF"
	case strings.HasPrefix(head, "<!doctype html"), strings.HasPrefix(head, "<html"), strings.Contains(head, "<html"):
		return "HTML"
	case len(b) == 0:
		return "empty body"
	default:
		return fmt.Sprintf("%d-byte body", len(b))
	}
}

func getURL(ctx context.Context, client *http.Client, rawURL, dst string, limit int64) (int64, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("User-Agent", homepageUserAgent)

	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return 0, nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return 0, nil, err
	}
	if int64(len(body)) > limit {
		return 0, nil, fmt.Errorf("response larger than %d bytes", limit)
	}
	if dst != "" {
		if err := os.WriteFile(dst, body, 0o644); err != nil {
			return 0, nil, err
		}
	}
	return int64(len(body)), body, nil
}

func renderHomepage(course canvas.Course, home string, files []hpFile, external []hpLink, errMsg string, manifest *Manifest) []byte {
	b := htmldoc.Open(course.Name+" 과목 홈페이지", home)

	var intro strings.Builder
	fmt.Fprintf(&intro, `<p><a href="%s">%s</a></p>`, htmldoc.Esc(home), htmldoc.Esc(home))
	intro.WriteString("<p>강의 슬라이드와 프로젝트 명세는 Canvas가 아니라 과목 홈페이지에 올라온다. 이 폴더는 그 사이트를 미러한 것이다.</p>")
	htmldoc.RichSection(b, "주소", intro.String(), nil)

	if errMsg != "" {
		htmldoc.Section(b, "가져오기 오류", errMsg)
	}

	if len(files) > 0 {
		var list strings.Builder
		list.WriteString("<ul>")
		for _, f := range files {
			href := f.URL
			if id := homepageEntryID(course.ID, f.URL); manifest != nil {
				if _, ok := manifest.Get(id); ok {
					href = fmt.Sprintf("/file/%d", id)
				}
			}
			label := f.Name
			if f.Size > 0 {
				label = fmt.Sprintf("%s (%s)", f.Name, humanBytes(f.Size))
			}
			fmt.Fprintf(&list, `<li><a href="%s">%s</a></li>`, htmldoc.Esc(href), htmldoc.Esc(label))
		}
		list.WriteString("</ul>")
		htmldoc.RichSection(b, "내려받은 파일", list.String(), nil)
	}

	if len(external) > 0 {
		var list strings.Builder
		list.WriteString("<ul>")
		for _, e := range external {
			label := e.Text
			if label == "" {
				label = e.URL
			}
			fmt.Fprintf(&list, `<li><a href="%s">%s</a></li>`, htmldoc.Esc(e.URL), htmldoc.Esc(label))
		}
		list.WriteString("</ul>")
		htmldoc.RichSection(b, "사이트에 걸린 외부 링크", list.String(), nil)
	}

	return htmldoc.Close(b, "과목 홈페이지에서 보관됨. 원본 목록은 같은 폴더의 homepage.json.")
}
