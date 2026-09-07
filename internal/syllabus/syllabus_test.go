package syllabus

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const canvasBody = `<div class="xn-btn-wrap"><button>한글</button></div>
<iframe class="xn-syllabus-iframe" src="https://sugang.snu.ac.kr/sugang/cc/cc103.action?openSchyy=2026&amp;openShtmFg=U000200002&amp;openDetaShtmFg=U000300001&amp;sbjtCd=4190.307&amp;ltNo=001&amp;sbjtSubhCd=000&amp;" width="100%"></iframe>`

func TestParseRefFromCanvasSyllabusBody(t *testing.T) {
	ref, ok := ParseRef(canvasBody)
	if !ok {
		t.Fatal("expected a reference")
	}
	want := Ref{Year: "2026", ShtmFg: "U000200002", DetaShtmFg: "U000300001",
		SbjtCd: "4190.307", LtNo: "001", SbjtSubhCd: "000"}
	if ref != want {
		t.Fatalf("ref = %+v, want %+v", ref, want)
	}
}

func TestParseRefRejectsBodyWithoutIframe(t *testing.T) {
	for _, body := range []string{"", "<p>no syllabus configured</p>", "<iframe src='https://example.com'></iframe>"} {
		if _, ok := ParseRef(body); ok {
			t.Errorf("ParseRef(%q) unexpectedly succeeded", body)
		}
	}
}

// fakeSugang mimics the two-step flow, including its trap: the data endpoint
// answers with blank fields unless the shell page primed the session.
type fakeSugang struct {
	srv          *httptest.Server
	primed       bool
	shellVisited bool
	requireShell bool
}

func newFakeSugang(t *testing.T, requireShell bool) *fakeSugang {
	f := &fakeSugang{requireShell: requireShell}
	mux := http.NewServeMux()

	mux.HandleFunc("/sugang/cc/cc100.action", func(w http.ResponseWriter, r *http.Request) {
		f.primed = true
		fmt.Fprint(w, "<html>entry</html>")
	})
	mux.HandleFunc("/sugang/cc/cc103.action", func(w http.ResponseWriter, r *http.Request) {
		f.shellVisited = true
		fmt.Fprint(w, "<html>shell</html>")
	})
	mux.HandleFunc("/sugang/cc/cc103ajax.action", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if f.requireShell && !f.shellVisited {
			// The real system returns a well-formed but empty record here,
			// which reads as "no syllabus" rather than an error.
			fmt.Fprint(w, `{"LISTTAB03":{},"LISTTAB03_LIST_ATTACH":[]}`)
			return
		}
		_ = r.ParseForm()
		fmt.Fprintf(w, `{
          "sbjtCd": %q,
          "LISTTAB01": {"lsnProgLang":"영어","lsnTmtablFormaSmryCtnt":"월(09:30~10:45)"},
          "LISTTAB03": {"sbjtNm":"운영체제","profNm":"이재욱","openPnt":3,"ltPurp":"운영체제 개요"},
          "LISTTAB03_LIST_MRKS": {"mid":30,"final":40,"homeWork":20,"attendance":10,"homeWorkRemk":"논문 요약"},
          "LISTTAB03_SUM_OF_MRKS": {"sumOfMrks":100},
          "LISTTAB03_LIST_WEEK_PLAN": [
            {"ltPlanDocWekClsfSeq":2,"wekClsfLtPlanCtnt":"프로세스"},
            {"ltPlanDocWekClsfSeq":1,"wekClsfLtPlanCtnt":"강의 소개"}
          ],
          "LISTTAB03_LIST_ATTACH": [
            {"korFileNm":null,"engFileNm":"OS강의계획서-영문.pdf (54.73KB)",
             "korAttachNo":"kor-uuid","engAttachNo":"eng-uuid",
             "korAttachDetaNo":null,"engAttachDetaNo":0}
          ]}`, r.Form.Get("sbjtCd"))
	})

	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func fakeClient(t *testing.T, f *fakeSugang) *Client {
	c, err := NewClient()
	if err != nil {
		t.Fatal(err)
	}
	c.baseURL = f.srv.URL
	return c
}

func TestFetchReturnsSyllabusAndAttachments(t *testing.T) {
	f := newFakeSugang(t, true)
	c := fakeClient(t, f)

	ref, _ := ParseRef(canvasBody)
	syl, err := c.Fetch(context.Background(), ref)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}

	if !f.primed {
		t.Error("session entry request was never made")
	}
	if !f.shellVisited {
		t.Error("shell page was never loaded; the data endpoint would return a blank record")
	}
	if got := syl.Raw["sbjtCd"]; got != "4190.307" {
		t.Errorf("form params not forwarded: sbjtCd=%v", got)
	}

	if len(syl.Attachments) != 1 {
		t.Fatalf("attachments = %d, want 1 (the Korean slot is null and must be skipped)", len(syl.Attachments))
	}
	a := syl.Attachments[0]
	if a.Name != "OS강의계획서-영문.pdf" {
		t.Errorf("name = %q; the trailing size suffix should be stripped", a.Name)
	}
	if a.FileNo != "eng-uuid" || a.Lang != "en" {
		t.Errorf("attachment = %+v", a)
	}
}

// The session is primed once and reused; the shell is loaded per course.
func TestFetchPrimesSessionOnlyOnce(t *testing.T) {
	f := newFakeSugang(t, false)
	c := fakeClient(t, f)
	ref, _ := ParseRef(canvasBody)

	for i := 0; i < 3; i++ {
		if _, err := c.Fetch(context.Background(), ref); err != nil {
			t.Fatalf("fetch %d: %v", i, err)
		}
	}
	if !c.primed {
		t.Error("client did not record priming")
	}
}

func TestFetchRejectsIncompleteRef(t *testing.T) {
	f := newFakeSugang(t, false)
	c := fakeClient(t, f)

	if _, err := c.Fetch(context.Background(), Ref{Year: "2026"}); err == nil {
		t.Fatal("expected an error for an incomplete reference")
	}
}

func TestDownloadAttachmentWritesFile(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("attcFileNo") != "eng-uuid" {
			t.Errorf("attcFileNo = %q", r.URL.Query().Get("attcFileNo"))
		}
		w.Header().Set("Content-Type", "application/octet;charset=utf-8")
		fmt.Fprint(w, "%PDF-1.3 syllabus")
	}))
	defer srv.Close()

	c, _ := NewClient()
	c.fileURL = srv.URL
	dst := filepath.Join(t.TempDir(), "강의계획서.pdf")

	n, err := c.DownloadAttachment(context.Background(), Attachment{Name: "x.pdf", FileNo: "eng-uuid"}, dst)
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	if n != 17 {
		t.Errorf("bytes = %d", n)
	}
	if b, _ := os.ReadFile(dst); !strings.HasPrefix(string(b), "%PDF") {
		t.Errorf("content = %q", b)
	}
	// No .part sidecars left behind.
	entries, _ := os.ReadDir(filepath.Dir(dst))
	if len(entries) != 1 {
		t.Errorf("expected one file, got %d", len(entries))
	}
}

// A bad request to the file host answers 200 with an HTML page. Archiving that
// as 강의계획서.pdf would leave an error page under a correct-looking name.
func TestDownloadAttachmentRejectsHTMLErrorPage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html;charset=UTF-8")
		fmt.Fprint(w, "<html><body>error</body></html>")
	}))
	defer srv.Close()

	c, _ := NewClient()
	c.fileURL = srv.URL
	dst := filepath.Join(t.TempDir(), "syllabus.pdf")

	if _, err := c.DownloadAttachment(context.Background(), Attachment{Name: "x.pdf", FileNo: "n"}, dst); err == nil {
		t.Fatal("expected an error for an HTML response")
	}
	if _, err := os.Stat(dst); err == nil {
		t.Fatal("HTML error page was archived")
	}
}

func TestDownloadAttachmentRejectsEmptyBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet")
	}))
	defer srv.Close()

	c, _ := NewClient()
	c.fileURL = srv.URL
	dst := filepath.Join(t.TempDir(), "syllabus.pdf")

	if _, err := c.DownloadAttachment(context.Background(), Attachment{Name: "x.pdf", FileNo: "n"}, dst); err == nil {
		t.Fatal("expected an error for an empty download")
	}
	if _, err := os.Stat(dst); err == nil {
		t.Fatal("empty download left a file behind")
	}
}

func TestRenderProducesReadableDocument(t *testing.T) {
	f := newFakeSugang(t, false)
	c := fakeClient(t, f)
	ref, _ := ParseRef(canvasBody)

	syl, err := c.Fetch(context.Background(), ref)
	if err != nil {
		t.Fatal(err)
	}
	out := string(Render("2026-2 운영체제 (001)", syl))

	for _, want := range []string{
		"운영체제", "이재욱", "운영체제 개요",
		"평가 방법", "중간고사", "30%", "논문 요약",
		"주차별 계획", "1주차", "강의 소개",
		"OS강의계획서-영문.pdf",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered syllabus missing %q", want)
		}
	}

	// Weeks must come out in order even though the API returned 2 before 1.
	if strings.Index(out, "1주차") > strings.Index(out, "2주차") {
		t.Error("week plan not sorted by week number")
	}
	// Empty fields must not render as blank rows.
	if strings.Contains(out, "<th>교재</th>") {
		t.Error("empty section rendered")
	}
}

func TestRenderEscapesHTML(t *testing.T) {
	syl := &Syllabus{Raw: map[string]any{
		"LISTTAB03": map[string]any{"sbjtNm": `<script>alert(1)</script>`, "ltPurp": "x"},
	}}
	out := string(Render("c", syl))
	if strings.Contains(out, "<script>alert(1)</script>") {
		t.Fatal("syllabus content was not escaped")
	}
}

// Free-text fields arrive with <br> embedded. Escaping them wholesale prints
// "<br>" to the reader; they must become line breaks while everything else
// stays escaped.
func TestRenderConvertsBrTagsButStillEscapes(t *testing.T) {
	syl := &Syllabus{Raw: map[string]any{
		"LISTTAB03": map[string]any{
			"sbjtNm": "테스트",
			"ltPurp": "첫 줄<br><br>둘째 줄<BR/>셋째 <b>줄</b> & 끝",
		},
	}}
	out := string(Render("c", syl))

	if strings.Contains(out, "&lt;br&gt;") || strings.Contains(out, "&lt;BR/&gt;") {
		t.Error("<br> rendered as visible text")
	}
	if !strings.Contains(out, "첫 줄\n\n둘째 줄\n셋째") {
		t.Error("<br> did not become a line break")
	}
	// Other markup must still be neutralised.
	if !strings.Contains(out, "&lt;b&gt;") {
		t.Error("other tags were not escaped")
	}
	if !strings.Contains(out, "&amp;") {
		t.Error("ampersand not escaped")
	}
}
