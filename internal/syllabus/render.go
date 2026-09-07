package syllabus

import (
	"bytes"
	"fmt"
	"html"
	"regexp"
	"sort"
	"strings"
)

// Render turns the syllabus record into a readable standalone HTML document.
//
// Most courses attach no syllabus file, so for them this rendering is the only
// artifact — the raw JSON is archived beside it so nothing is lost if a field
// here is missed.
func Render(courseName string, s *Syllabus) []byte {
	t1, _ := s.Raw["LISTTAB01"].(map[string]any)
	t3, _ := s.Raw["LISTTAB03"].(map[string]any)
	if t1 == nil {
		t1 = map[string]any{}
	}
	if t3 == nil {
		t3 = map[string]any{}
	}

	var b bytes.Buffer
	title := firstNonEmpty(str(t3["sbjtNm"]), str(t3["sbjtEngNm"]), courseName)

	fmt.Fprintf(&b, `<!doctype html>
<html lang="ko"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="color-scheme" content="light dark">
<title>%s 강의계획서</title>
<style>
:root{--bg:#fbfaf8;--panel:#fff;--ink:#1b1a17;--muted:#6b675f;--line:#e5e1d9;--accent:#8a5a2b}
@media(prefers-color-scheme:dark){:root{--bg:#16151a;--panel:#1e1d23;--ink:#ece9e3;--muted:#9b968d;--line:#302e37;--accent:#d9a441}}
*{box-sizing:border-box}
body{margin:0;background:var(--bg);color:var(--ink);font:15px/1.6 -apple-system,BlinkMacSystemFont,"Apple SD Gothic Neo","Noto Sans KR",system-ui,sans-serif}
.wrap{max-width:820px;margin:0 auto;padding:24px 16px 64px}
h1{font-size:22px;margin:0 0 2px;letter-spacing:-.01em}
.sub{color:var(--muted);font-size:14px;margin-bottom:22px}
h2{font-size:13px;text-transform:uppercase;letter-spacing:.08em;color:var(--muted);margin:30px 0 10px}
table{width:100%%;border-collapse:collapse;background:var(--panel);border:1px solid var(--line);border-radius:10px;overflow:hidden}
th,td{text-align:left;padding:9px 12px;border-bottom:1px solid var(--line);vertical-align:top}
tr:last-child th,tr:last-child td{border-bottom:0}
th{width:30%%;color:var(--muted);font-weight:600;white-space:nowrap}
td.wk{width:64px;color:var(--muted);white-space:nowrap}
p.body{white-space:pre-wrap;background:var(--panel);border:1px solid var(--line);border-radius:10px;padding:12px 14px;margin:0}
.files a{display:block;padding:9px 12px;background:var(--panel);border:1px solid var(--line);border-radius:10px;margin-bottom:6px;color:var(--accent);text-decoration:none}
footer{margin-top:34px;color:var(--muted);font-size:12px}
</style></head><body><div class="wrap">
<h1>%s</h1>
<div class="sub">%s</div>
`, esc(title), esc(title), esc(courseName))

	// Header facts
	rows := [][2]string{
		{"교과목번호", joinNonEmpty(" ", str(t3["sbjtCd"]), str(t3["ltNo"]))},
		{"영문명", str(t3["sbjtEngNm"])},
		{"개설학과", str(t3["sbjtMngtDeptNm"])},
		{"학점", str(t3["openPnt"])},
		{"교수", joinNonEmpty(" ", str(t3["profNm"]), parens(str(t3["wkgdNm"])))},
		{"이메일", firstNonEmpty(str(t3["profEmail"]), str(t3["profEmail2"]))},
		{"면담", str(t3["stdIntrvPl"])},
		{"강의시간", str(t1["lsnTmtablFormaSmryCtnt"])},
		{"수업언어", str(t1["lsnProgLang"])},
		{"수업형태", str(t1["lsnProgType"])},
		{"평가", joinNonEmpty(" · ", str(t3["mrksRelevalYnNm"]), str(t3["mrksGvMthdFgNm"]))},
	}
	writeTable(&b, rows)

	section(&b, "교과목 개요", firstNonEmpty(str(t3["ltPurp"]), str(t3["ltEngPurp"])))
	section(&b, "수강 요건 / 비고", firstNonEmpty(str(t3["ltPlanDocRemk"]), str(t3["ltPlanDocEngRemk"])))
	section(&b, "생성형 AI 활용", firstNonEmpty(str(t3["genrAiUtlzKorCtnt"]), str(t3["genrAiUtlzEngCtnt"])))

	writeGrading(&b, s.Raw)
	writeWeeks(&b, s.Raw)
	writeBooks(&b, s.Raw)

	if len(s.Attachments) > 0 {
		b.WriteString(`<h2>첨부 강의계획서</h2><div class="files">`)
		for _, a := range s.Attachments {
			fmt.Fprintf(&b, `<a href="%s">%s</a>`, esc(a.Name), esc(a.Name))
		}
		b.WriteString(`</div>`)
	}

	fmt.Fprintf(&b, `<footer>sugang.snu.ac.kr · %s-%s %s-%s 에서 보관됨. 원본 데이터는 같은 폴더의 강의계획서.json 참고.</footer>
</div></body></html>`,
		esc(s.Ref.Year), esc(s.Ref.ShtmFg), esc(s.Ref.SbjtCd), esc(s.Ref.LtNo))

	return b.Bytes()
}

func writeTable(b *bytes.Buffer, rows [][2]string) {
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
		fmt.Fprintf(b, "<tr><th>%s</th><td>%s</td></tr>", esc(r[0]), esc(r[1]))
	}
	b.WriteString("</table>")
}

func writeGrading(b *bytes.Buffer, raw map[string]any) {
	m, _ := raw["LISTTAB03_LIST_MRKS"].(map[string]any)
	if m == nil {
		return
	}

	labels := []struct{ key, remk, label string }{
		{"attendance", "attendanceRemk", "출석"},
		{"attitude", "attitudeRemk", "태도"},
		{"quiz", "quizRemk", "퀴즈"},
		{"homeWork", "homeWorkRemk", "과제"},
		{"mid", "midRemk", "중간고사"},
		{"final", "finalRemk", "기말고사"},
		{"etc", "etcRemk", "기타"},
	}

	var rows [][2]string
	for _, l := range labels {
		v := str(m[l.key])
		if v == "" || v == "0" {
			continue
		}
		rows = append(rows, [2]string{l.label, joinNonEmpty(" — ", v+"%", str(m[l.remk]))})
	}
	if len(rows) == 0 {
		return
	}
	if sum, ok := raw["LISTTAB03_SUM_OF_MRKS"].(map[string]any); ok {
		if s := str(sum["sumOfMrks"]); s != "" {
			rows = append(rows, [2]string{"합계", s + "%"})
		}
	}

	b.WriteString("<h2>평가 방법</h2>")
	writeTable(b, rows)
}

func writeWeeks(b *bytes.Buffer, raw map[string]any) {
	list, _ := raw["LISTTAB03_LIST_WEEK_PLAN"].([]any)
	if len(list) == 0 {
		return
	}

	type week struct {
		n    int
		text string
	}
	var weeks []week
	for _, r := range list {
		row, ok := r.(map[string]any)
		if !ok {
			continue
		}
		text := firstNonEmpty(str(row["wekClsfLtPlanCtnt"]), str(row["wekClsfLtPlanEngCtnt"]))
		if text == "" {
			continue
		}
		n := 0
		fmt.Sscanf(str(row["ltPlanDocWekClsfSeq"]), "%d", &n)
		weeks = append(weeks, week{n: n, text: text})
	}
	if len(weeks) == 0 {
		return
	}
	sort.Slice(weeks, func(i, j int) bool { return weeks[i].n < weeks[j].n })

	b.WriteString("<h2>주차별 계획</h2><table>")
	for _, w := range weeks {
		fmt.Fprintf(b, `<tr><td class="wk">%d주차</td><td>%s</td></tr>`, w.n, esc(w.text))
	}
	b.WriteString("</table>")
}

func writeBooks(b *bytes.Buffer, raw map[string]any) {
	list, _ := raw["LISTTAB03_LIST_TEACHM_BOOK"].([]any)
	if len(list) == 0 {
		return
	}

	var rows [][2]string
	for _, r := range list {
		row, ok := r.(map[string]any)
		if !ok {
			continue
		}
		name := firstNonEmpty(str(row["teachmBookNm"]), str(row["bookNm"]), str(row["teachmNm"]))
		if name == "" {
			continue
		}
		rows = append(rows, [2]string{
			firstNonEmpty(str(row["teachmClsfNm"]), "교재"),
			joinNonEmpty(" · ", name, str(row["authorNm"]), str(row["pblcoNm"])),
		})
	}
	if len(rows) == 0 {
		return
	}
	b.WriteString("<h2>교재</h2>")
	writeTable(b, rows)
}

func section(b *bytes.Buffer, heading, body string) {
	if strings.TrimSpace(body) == "" {
		return
	}
	fmt.Fprintf(b, `<h2>%s</h2><p class="body">%s</p>`, esc(heading), escBody(body))
}

func esc(s string) string { return html.EscapeString(s) }

var brRe = regexp.MustCompile(`(?i)<br\s*/?>`)

// escBody escapes free-text fields, which arrive with <br> tags embedded in
// otherwise plain text. Escaping them wholesale would print "<br>" to the
// reader; the block renders with white-space:pre-wrap, so a newline is the
// faithful substitute. Everything else is still escaped.
func escBody(s string) string {
	return html.EscapeString(brRe.ReplaceAllString(s, "\n"))
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func joinNonEmpty(sep string, vals ...string) string {
	var kept []string
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			kept = append(kept, strings.TrimSpace(v))
		}
	}
	return strings.Join(kept, sep)
}

func parens(s string) string {
	if strings.TrimSpace(s) == "" {
		return ""
	}
	return "(" + strings.TrimSpace(s) + ")"
}
