// Package syllabus retrieves SNU 강의계획서 (course syllabi).
//
// Canvas does not hold them. A course's syllabus_body on myETL is a shell
// wrapping an iframe that points at sugang.snu.ac.kr, so the syllabus has to be
// fetched from that system: load the iframe page to establish per-course
// session state, then POST to its data endpoint. Skipping the page load returns
// a valid-looking but empty record.
package syllabus

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const (
	sugangBase = "https://sugang.snu.ac.kr"
	shellPath  = "/sugang/cc/cc103.action"
	dataPath   = "/sugang/cc/cc103ajax.action"
	entryPath  = "/sugang/cc/cc100.action"
	filePath   = "https://shine.snu.ac.kr/com/download/noflt/downloadWebFile.action"
	userAgent  = "Mozilla/5.0 (compatible; lx-agent)"
)

// Ref identifies one course offering in the sugang system.
type Ref struct {
	Year       string
	ShtmFg     string
	DetaShtmFg string
	SbjtCd     string
	LtNo       string
	SbjtSubhCd string
}

func (r Ref) values() url.Values {
	return url.Values{
		"openSchyy":      {r.Year},
		"openShtmFg":     {r.ShtmFg},
		"openDetaShtmFg": {r.DetaShtmFg},
		"sbjtCd":         {r.SbjtCd},
		"ltNo":           {r.LtNo},
		"sbjtSubhCd":     {r.SbjtSubhCd},
	}
}

func (r Ref) valid() bool {
	return r.Year != "" && r.ShtmFg != "" && r.SbjtCd != ""
}

// Attachment is a syllabus document uploaded by the instructor.
type Attachment struct {
	Name   string
	FileNo string
	DetaNo string
	Lang   string // "ko" or "en"
}

// Syllabus is one course's retrieved syllabus.
type Syllabus struct {
	Ref         Ref
	Raw         map[string]any
	RawJSON     []byte
	Attachments []Attachment
}

type Client struct {
	http    *http.Client
	primed  bool
	baseURL string
	fileURL string
}

func NewClient() (*Client, error) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, err
	}
	return &Client{
		http:    &http.Client{Jar: jar, Timeout: 45 * time.Second},
		baseURL: sugangBase,
		fileURL: filePath,
	}, nil
}

var iframeRe = regexp.MustCompile(`cc103\.action\?([^"'\s>]+)`)

// ParseRef pulls the course identifiers out of a Canvas syllabus_body.
func ParseRef(syllabusBody string) (Ref, bool) {
	m := iframeRe.FindStringSubmatch(syllabusBody)
	if len(m) < 2 {
		return Ref{}, false
	}

	q, err := url.ParseQuery(strings.ReplaceAll(strings.TrimRight(m[1], `"'`), "&amp;", "&"))
	if err != nil {
		return Ref{}, false
	}

	ref := Ref{
		Year:       q.Get("openSchyy"),
		ShtmFg:     q.Get("openShtmFg"),
		DetaShtmFg: q.Get("openDetaShtmFg"),
		SbjtCd:     q.Get("sbjtCd"),
		LtNo:       q.Get("ltNo"),
		SbjtSubhCd: q.Get("sbjtSubhCd"),
	}
	return ref, ref.valid()
}

// Fetch retrieves one course's syllabus.
func (c *Client) Fetch(ctx context.Context, ref Ref) (*Syllabus, error) {
	if !ref.valid() {
		return nil, fmt.Errorf("incomplete syllabus reference")
	}
	if err := c.prime(ctx); err != nil {
		return nil, err
	}

	// The shell page is not optional. The data endpoint keys off session state
	// this request establishes; without it the response is well-formed JSON
	// with every field blank, which reads as "no syllabus" rather than an error.
	shell := c.baseURL + shellPath + "?" + ref.values().Encode() + "&lang_knd=ko"
	if err := c.discard(ctx, http.MethodGet, shell, nil, ""); err != nil {
		return nil, fmt.Errorf("syllabus shell: %w", err)
	}

	form := ref.values()
	form.Set("isSnuGenie", "N")

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+dataPath,
		strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	req.Header.Set("Referer", c.baseURL+shellPath)
	req.Header.Set("User-Agent", userAgent)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("syllabus data: HTTP %d", resp.StatusCode)
	}

	var data map[string]any
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, fmt.Errorf("syllabus data was not JSON (session likely rejected): %w", err)
	}

	return &Syllabus{
		Ref:         ref,
		Raw:         data,
		RawJSON:     raw,
		Attachments: attachmentsOf(data),
	}, nil
}

// prime performs the one-time entry request that mints a sugang session.
func (c *Client) prime(ctx context.Context) error {
	if c.primed {
		return nil
	}
	if err := c.discard(ctx, http.MethodGet, c.baseURL+entryPath, nil, ""); err != nil {
		return fmt.Errorf("sugang session: %w", err)
	}
	c.primed = true
	return nil
}

func (c *Client) discard(ctx context.Context, method, url string, body io.Reader, referer string) error {
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", userAgent)
	if referer != "" {
		req.Header.Set("Referer", referer)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	return nil
}

// DownloadAttachment writes a syllabus document to dst. These live on
// shine.snu.ac.kr and need no authentication.
func (c *Client) DownloadAttachment(ctx context.Context, a Attachment, dst string) (int64, error) {
	q := url.Values{
		"cscLocale":      {"ko_KR"},
		"attcFileNo":     {a.FileNo},
		"attcFileDetaNo": {a.DetaNo},
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.fileURL+"?"+q.Encode(), nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("User-Agent", userAgent)

	resp, err := c.http.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return 0, fmt.Errorf("attachment %s: HTTP %d", a.Name, resp.StatusCode)
	}
	// The file host answers a bad request with a 200 and an HTML page. Saving
	// that as 강의계획서.pdf would archive an error page under a name that
	// looks correct forever after.
	if ct := resp.Header.Get("Content-Type"); strings.HasPrefix(strings.ToLower(ct), "text/html") {
		return 0, fmt.Errorf("attachment %s: server returned an HTML page, not a file", a.Name)
	}

	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return 0, err
	}
	f, err := os.CreateTemp(filepath.Dir(dst), ".syllabus-*.part")
	if err != nil {
		return 0, err
	}
	tmp := f.Name()

	n, err := io.Copy(f, resp.Body)
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		os.Remove(tmp)
		if err == nil {
			err = closeErr
		}
		return 0, err
	}
	if n == 0 {
		os.Remove(tmp)
		return 0, fmt.Errorf("attachment %s was empty", a.Name)
	}
	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(tmp)
		return 0, err
	}
	return n, nil
}

// attachmentsOf reads the attachment list, which carries a Korean and an
// English document under separate ids in the same row.
func attachmentsOf(data map[string]any) []Attachment {
	rows, _ := data["LISTTAB03_LIST_ATTACH"].([]any)

	var out []Attachment
	for _, r := range rows {
		row, ok := r.(map[string]any)
		if !ok {
			continue
		}
		for _, v := range []struct{ name, no, deta, lang string }{
			{"korFileNm", "korAttachNo", "korAttachDetaNo", "ko"},
			{"engFileNm", "engAttachNo", "engAttachDetaNo", "en"},
		} {
			name := str(row[v.name])
			no := str(row[v.no])
			if name == "" || no == "" {
				continue
			}
			out = append(out, Attachment{
				Name:   cleanName(name),
				FileNo: no,
				DetaNo: str(row[v.deta]),
				Lang:   v.lang,
			})
		}
	}
	return out
}

// cleanName strips the trailing "(54.73KB)" the API appends to file names.
var sizeSuffixRe = regexp.MustCompile(`\s*\([\d.]+\s*[KMG]?B\)\s*$`)

func cleanName(name string) string {
	return strings.TrimSpace(sizeSuffixRe.ReplaceAllString(strings.TrimSpace(name), ""))
}

func str(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(t)
	case float64:
		if t == float64(int64(t)) {
			return fmt.Sprintf("%d", int64(t))
		}
		return fmt.Sprintf("%v", t)
	default:
		return strings.TrimSpace(fmt.Sprintf("%v", t))
	}
}
