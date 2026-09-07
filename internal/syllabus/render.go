package syllabus

import (
	"bytes"
	"fmt"
	"sort"
	"strings"

	"github.com/mgnlia/lx-agent/internal/htmldoc"
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

	title := firstNonEmpty(str(t3["sbjtNm"]), str(t3["sbjtEngNm"]), courseName)
	b := htmldoc.Open(title+" 강의계획서", courseName)

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
	htmldoc.Table(b, rows)

	htmldoc.Section(b, "교과목 개요", firstNonEmpty(str(t3["ltPurp"]), str(t3["ltEngPurp"])))
	htmldoc.Section(b, "수강 요건 / 비고", firstNonEmpty(str(t3["ltPlanDocRemk"]), str(t3["ltPlanDocEngRemk"])))
	htmldoc.Section(b, "생성형 AI 활용", firstNonEmpty(str(t3["genrAiUtlzKorCtnt"]), str(t3["genrAiUtlzEngCtnt"])))

	writeGrading(b, s.Raw)
	writeWeeks(b, s.Raw)
	writeBooks(b, s.Raw)

	if len(s.Attachments) > 0 {
		htmldoc.Heading(b, "첨부 강의계획서")
		b.WriteString(`<div class="files">`)
		for _, a := range s.Attachments {
			fmt.Fprintf(b, `<a href="%s">%s</a>`, htmldoc.Esc(a.Name), htmldoc.Esc(a.Name))
		}
		b.WriteString(`</div>`)
	}

	return htmldoc.Close(b, fmt.Sprintf(
		"sugang.snu.ac.kr · %s-%s %s-%s 에서 보관됨. 원본 데이터는 같은 폴더의 강의계획서.json 참고.",
		s.Ref.Year, s.Ref.ShtmFg, s.Ref.SbjtCd, s.Ref.LtNo))
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

	htmldoc.Heading(b, "평가 방법")
	htmldoc.Table(b, rows)
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

	htmldoc.Heading(b, "주차별 계획")
	b.WriteString("<table>")
	for _, w := range weeks {
		fmt.Fprintf(b, `<tr><td class="wk">%d주차</td><td>%s</td></tr>`, w.n, htmldoc.Esc(w.text))
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
	htmldoc.Heading(b, "교재")
	htmldoc.Table(b, rows)
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
