package monitor

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/mgnlia/lx-agent/internal/binding"
	"github.com/mgnlia/lx-agent/internal/canvas"
	"github.com/mgnlia/lx-agent/internal/notifier"
	"github.com/mgnlia/lx-agent/internal/summarizer"
)

type Config struct {
	PollInterval   time.Duration
	CourseFilter   []int // empty = all courses
	SummarizeNew   bool
	DeadlineAlerts []int // days before due (e.g., [3, 1, 0])
	StatePath      string
	DatabaseURL    string
	ChatID         string
}

type Alert struct {
	Type      string
	CourseID  int
	EntityID  *int64
	DedupeKey string
	Message   string
	Metadata  map[string]any
}

type Monitor struct {
	client     *canvas.Client
	notifier   notifier.Notifier
	summarizer summarizer.Summarizer
	config     Config
	state      *State
	logger     *slog.Logger
	store      *binding.Store
	chatID     string
}

func New(
	client *canvas.Client,
	n notifier.Notifier,
	s summarizer.Summarizer,
	cfg Config,
	logger *slog.Logger,
) *Monitor {
	if cfg.StatePath == "" {
		cfg.StatePath = "lx-state.json"
	}
	if len(cfg.DeadlineAlerts) == 0 {
		cfg.DeadlineAlerts = []int{3, 1, 0}
	}

	state := NewState(cfg.StatePath)
	if err := state.Load(); err != nil {
		logger.Warn("load state failed", "err", err)
	}

	var store *binding.Store
	if strings.TrimSpace(cfg.DatabaseURL) != "" && strings.TrimSpace(cfg.ChatID) != "" {
		s, err := binding.New(cfg.DatabaseURL)
		if err != nil {
			logger.Warn("init monitor binding store failed", "err", err)
		} else if err := s.EnsureSchema(context.Background()); err != nil {
			logger.Warn("ensure monitor binding schema failed", "err", err)
			_ = s.Close()
		} else {
			store = s
		}
	}

	return &Monitor{
		client:     client,
		notifier:   n,
		summarizer: s,
		config:     cfg,
		state:      state,
		logger:     logger,
		store:      store,
		chatID:     cfg.ChatID,
	}
}

func (m *Monitor) closeStore() {
	if m.store != nil {
		_ = m.store.Close()
		m.store = nil
	}
}

func (m *Monitor) Run(ctx context.Context) error {
	defer m.closeStore()
	m.logger.Info("starting monitor", "interval", m.config.PollInterval)

	// Initial check
	if err := m.check(ctx); err != nil {
		m.logger.Error("check failed", "err", err)
	}

	ticker := time.NewTicker(m.config.PollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			m.logger.Info("monitor stopped")
			return ctx.Err()
		case <-ticker.C:
			if err := m.check(ctx); err != nil {
				m.logger.Error("check failed", "err", err)
			}
		}
	}
}

func (m *Monitor) RunOnce(ctx context.Context) error {
	defer m.closeStore()
	return m.check(ctx)
}

func applyCourseFilter(courses []canvas.Course, ids []int) []canvas.Course {
	if len(ids) == 0 {
		return courses
	}
	filterSet := make(map[int]bool, len(ids))
	for _, id := range ids {
		filterSet[id] = true
	}
	var filtered []canvas.Course
	for _, c := range courses {
		if filterSet[c.ID] {
			filtered = append(filtered, c)
		}
	}
	return filtered
}

func currentSemesterKeyKST(now time.Time) string {
	kst := now.In(time.FixedZone("KST", 9*3600))
	semester := 1
	if int(kst.Month()) >= 8 {
		semester = 2
	}
	return fmt.Sprintf("%04d-%d", kst.Year(), semester)
}

func courseMatchesSemester(course canvas.Course, semesterKey string) bool {
	targets := []string{
		strings.ToLower(course.Name),
		strings.ToLower(course.CourseCode),
	}
	patterns := []string{
		strings.ToLower(semesterKey),
		strings.ToLower(strings.ReplaceAll(semesterKey, "-", " ")),
		strings.ToLower(strings.ReplaceAll(semesterKey, "-", "_")),
	}
	for _, t := range targets {
		for _, p := range patterns {
			if p != "" && strings.Contains(t, p) {
				return true
			}
		}
	}
	return false
}

func filterCoursesBySemester(courses []canvas.Course, semesterKey string) []canvas.Course {
	var out []canvas.Course
	for _, c := range courses {
		if courseMatchesSemester(c, semesterKey) {
			out = append(out, c)
		}
	}
	return out
}

func defaultSemesterCourses(courses []canvas.Course) ([]canvas.Course, string) {
	semesterKey := currentSemesterKeyKST(time.Now())
	filtered := filterCoursesBySemester(courses, semesterKey)
	if len(filtered) == 0 {
		return courses, semesterKey
	}
	return filtered, semesterKey
}

func parseContextCourseID(contextCode string) int {
	if !strings.HasPrefix(contextCode, "course_") {
		return 0
	}
	id, err := strconv.Atoi(strings.TrimPrefix(contextCode, "course_"))
	if err != nil {
		return 0
	}
	return id
}

func (m *Monitor) check(ctx context.Context) error {
	m.logger.Info("running check")

	courses, err := m.client.GetCourses(ctx)
	if err != nil {
		return fmt.Errorf("get courses: %w", err)
	}

	courses = applyCourseFilter(courses, m.config.CourseFilter)

	hasExplicitCourseScope := len(m.config.CourseFilter) > 0
	if m.store != nil && m.chatID != "" {
		subscribedCourseIDs, err := m.store.ListChatCourses(ctx, m.chatID)
		if err != nil {
			m.logger.Warn("load subscribed chat courses failed", "chat_id", m.chatID, "err", err)
		} else if len(subscribedCourseIDs) > 0 {
			courses = applyCourseFilter(courses, subscribedCourseIDs)
			hasExplicitCourseScope = true
		}
	}
	if !hasExplicitCourseScope {
		filtered, semesterKey := defaultSemesterCourses(courses)
		courses = filtered
		m.logger.Info("using default semester scope", "semester", semesterKey, "course_count", len(courses))
	}

	m.logger.Info("checking courses", "count", len(courses))

	var alerts []Alert

	for _, course := range courses {
		newFiles, err := m.checkFiles(ctx, course)
		if err != nil {
			m.logger.Error("check files failed", "course", course.Name, "err", err)
		}
		alerts = append(alerts, newFiles...)

		newAssignments, err := m.checkAssignments(ctx, course)
		if err != nil {
			m.logger.Error("check assignments failed", "course", course.Name, "err", err)
		}
		alerts = append(alerts, newAssignments...)

		deadlines, err := m.checkDeadlines(ctx, course)
		if err != nil {
			m.logger.Error("check deadlines failed", "course", course.Name, "err", err)
		}
		alerts = append(alerts, deadlines...)
	}

	courseIDs := make([]int, len(courses))
	for i, c := range courses {
		courseIDs[i] = c.ID
	}
	if len(courseIDs) > 0 {
		newAnnouncements, err := m.checkAnnouncements(ctx, courseIDs, courses)
		if err != nil {
			m.logger.Error("check announcements failed", "err", err)
		}
		alerts = append(alerts, newAnnouncements...)
	}

	sentCount := 0
	skippedDuplicates := 0
	for _, alert := range alerts {
		recordInserted := true
		if m.store != nil && m.chatID != "" {
			recordInserted, err = m.store.InsertSentAlertIfNew(ctx, m.chatID, binding.SentAlert{
				DedupeKey: alert.DedupeKey,
				AlertType: alert.Type,
				CourseID:  intPtrIfNonZero(alert.CourseID),
				EntityID:  alert.EntityID,
				Metadata:  alert.Metadata,
			})
			if err != nil {
				m.logger.Warn("sent-alert dedupe check failed; sending anyway", "err", err, "dedupe_key", alert.DedupeKey)
				recordInserted = true
			}
		}
		if !recordInserted {
			skippedDuplicates++
			continue
		}

		if err := m.notifier.Send(ctx, alert.Message); err != nil {
			m.logger.Error("notify failed", "err", err)
			if m.store != nil && m.chatID != "" {
				if rmErr := m.store.DeleteSentAlert(ctx, m.chatID, alert.DedupeKey); rmErr != nil {
					m.logger.Warn("rollback sent-alert marker failed", "err", rmErr, "dedupe_key", alert.DedupeKey)
				}
			}
			continue
		}
		sentCount++
	}

	m.state.Data.LastCheck = time.Now()
	if err := m.state.Save(); err != nil {
		m.logger.Warn("save state failed", "err", err)
	}

	if sentCount > 0 {
		m.logger.Info("sent notifications", "count", sentCount, "skipped_duplicates", skippedDuplicates)
	} else {
		m.logger.Info("no updates", "skipped_duplicates", skippedDuplicates)
	}

	return nil
}

func (m *Monitor) checkFiles(ctx context.Context, course canvas.Course) ([]Alert, error) {
	files, err := m.client.GetFiles(ctx, course.ID)
	if err != nil {
		return nil, err
	}

	var alerts []Alert
	for _, f := range files {
		if !m.state.IsFileNew(f.ID, f.Size) {
			continue
		}
		m.state.MarkFile(f.ID, f.Size)

		msg := fmt.Sprintf("📄 *새 강의자료*\n📚 %s\n📎 %s (%s)",
			course.Name, f.DisplayName, humanSize(f.Size))

		if m.summarizer != nil && m.config.SummarizeNew {
			summary, err := m.summarizeFile(ctx, f)
			if err != nil {
				m.logger.Warn("summarize failed", "file", f.DisplayName, "err", err)
			} else if summary != "" {
				msg += "\n\n📝 *요약:*\n" + summary
			}
		}

		entityID := int64(f.ID)
		alerts = append(alerts, Alert{
			Type:      "file",
			CourseID:  course.ID,
			EntityID:  &entityID,
			DedupeKey: fmt.Sprintf("file:%d:%d", f.ID, f.Size),
			Message:   msg,
			Metadata: map[string]any{
				"course_id":    course.ID,
				"course_name":  course.Name,
				"file_id":      f.ID,
				"file_name":    f.DisplayName,
				"file_size":    f.Size,
				"file_updated": f.UpdatedAt.UTC().Format(time.RFC3339),
			},
		})
	}

	return alerts, nil
}

func (m *Monitor) checkAssignments(ctx context.Context, course canvas.Course) ([]Alert, error) {
	assignments, err := m.client.GetAssignments(ctx, course.ID)
	if err != nil {
		return nil, err
	}

	var alerts []Alert
	for _, a := range assignments {
		if !m.state.IsAssignmentNew(a.ID) {
			continue
		}
		m.state.MarkAssignment(a.ID)

		due := "마감일 없음"
		dueAt := ""
		if a.DueAt != nil {
			due = a.DueAt.In(time.FixedZone("KST", 9*3600)).Format("2006-01-02 15:04 KST")
			dueAt = a.DueAt.UTC().Format(time.RFC3339)
		}

		msg := fmt.Sprintf("📝 *새 과제*\n📚 %s\n📌 %s\n⏰ %s\n💯 %.0f점",
			course.Name, a.Name, due, a.PointsPossible)

		entityID := int64(a.ID)
		alerts = append(alerts, Alert{
			Type:      "assignment",
			CourseID:  course.ID,
			EntityID:  &entityID,
			DedupeKey: fmt.Sprintf("assignment:%d", a.ID),
			Message:   msg,
			Metadata: map[string]any{
				"course_id":     course.ID,
				"course_name":   course.Name,
				"assignment_id": a.ID,
				"title":         a.Name,
				"due_at":        dueAt,
				"points":        a.PointsPossible,
			},
		})
	}

	return alerts, nil
}

func (m *Monitor) checkDeadlines(ctx context.Context, course canvas.Course) ([]Alert, error) {
	assignments, err := m.client.GetAssignments(ctx, course.ID)
	if err != nil {
		return nil, err
	}

	var alerts []Alert

	for _, a := range assignments {
		if a.DueAt == nil || a.Submitted {
			continue
		}

		until := time.Until(*a.DueAt)
		if until < 0 {
			continue
		}

		daysLeft := int(until.Hours() / 24)

		for _, alertDay := range m.config.DeadlineAlerts {
			if daysLeft <= alertDay {
				level := fmt.Sprintf("D-%d", alertDay)
				if alertDay == 0 {
					level = "D-DAY"
				}

				if !m.state.ShouldAlertDeadline(a.ID, level) {
					continue
				}
				m.state.MarkDeadlineAlert(a.ID, level)

				kst := a.DueAt.In(time.FixedZone("KST", 9*3600))
				hoursLeft := int(until.Hours())

				var urgency string
				if alertDay == 0 {
					urgency = fmt.Sprintf("🔴 *%s* — %d시간 남음!", level, hoursLeft)
				} else if alertDay == 1 {
					urgency = fmt.Sprintf("🟡 *%s* — 내일 마감!", level)
				} else {
					urgency = fmt.Sprintf("🟢 *%s* — %d일 남음", level, daysLeft)
				}

				msg := fmt.Sprintf("⏰ *과제 마감 알림*\n📚 %s\n📌 %s\n%s\n📅 %s",
					course.Name, a.Name, urgency,
					kst.Format("2006-01-02 15:04 KST"))

				entityID := int64(a.ID)
				alerts = append(alerts, Alert{
					Type:      "deadline",
					CourseID:  course.ID,
					EntityID:  &entityID,
					DedupeKey: fmt.Sprintf("deadline:%d:%s", a.ID, level),
					Message:   msg,
					Metadata: map[string]any{
						"course_id":     course.ID,
						"course_name":   course.Name,
						"assignment_id": a.ID,
						"title":         a.Name,
						"due_at":        a.DueAt.UTC().Format(time.RFC3339),
						"level":         level,
						"days_left":     daysLeft,
						"hours_left":    hoursLeft,
					},
				})
				break
			}
		}
	}

	return alerts, nil
}

func (m *Monitor) checkAnnouncements(ctx context.Context, courseIDs []int, courses []canvas.Course) ([]Alert, error) {
	announcements, err := m.client.GetAnnouncements(ctx, courseIDs)
	if err != nil {
		return nil, err
	}

	courseNameMap := make(map[string]string)
	courseIDMap := make(map[string]int)
	for _, c := range courses {
		key := fmt.Sprintf("course_%d", c.ID)
		courseNameMap[key] = c.Name
		courseIDMap[key] = c.ID
	}

	var alerts []Alert
	for _, a := range announcements {
		if !m.state.IsAnnouncementNew(a.ID) {
			continue
		}
		m.state.MarkAnnouncement(a.ID)

		courseName := courseNameMap[a.ContextCode]
		if courseName == "" {
			courseName = a.ContextCode
		}
		courseID := courseIDMap[a.ContextCode]
		if courseID == 0 {
			courseID = parseContextCourseID(a.ContextCode)
		}

		plainMsg := stripHTML(a.Message)
		if len(plainMsg) > 500 {
			plainMsg = plainMsg[:500] + "..."
		}

		msg := fmt.Sprintf("📢 *새 공지*\n📚 %s\n📌 %s\n\n%s",
			courseName, a.Title, plainMsg)

		if m.summarizer != nil && len(a.Message) > 200 {
			summary, err := m.summarizer.SummarizeText(ctx, a.Title, plainMsg)
			if err == nil && summary != "" {
				msg += "\n\n📝 *요약:*\n" + summary
			}
		}

		entityID := int64(a.ID)
		alerts = append(alerts, Alert{
			Type:      "announcement",
			CourseID:  courseID,
			EntityID:  &entityID,
			DedupeKey: fmt.Sprintf("announcement:%d", a.ID),
			Message:   msg,
			Metadata: map[string]any{
				"course_id":         courseID,
				"course_name":       courseName,
				"announcement_id":   a.ID,
				"title":             a.Title,
				"posted_at":         a.PostedAt.UTC().Format(time.RFC3339),
				"context_code":      a.ContextCode,
				"announcement_link": a.HTMLURL,
			},
		})
	}

	return alerts, nil
}

func (m *Monitor) summarizeFile(ctx context.Context, f canvas.File) (string, error) {
	// Only summarize reasonable file types and sizes
	lower := strings.ToLower(f.DisplayName)
	if !strings.HasSuffix(lower, ".pdf") && !strings.HasSuffix(lower, ".pptx") &&
		!strings.HasSuffix(lower, ".txt") && !strings.HasSuffix(lower, ".md") &&
		!strings.HasSuffix(lower, ".docx") {
		return "", nil
	}

	if f.Size > 50*1024*1024 { // 50MB limit
		return "", nil
	}

	data, err := m.client.DownloadFile(ctx, f.URL)
	if err != nil {
		return "", err
	}

	return m.summarizer.SummarizeFile(ctx, f.DisplayName, data)
}

func intPtrIfNonZero(v int) *int {
	if v == 0 {
		return nil
	}
	x := v
	return &x
}

var htmlTagRe = regexp.MustCompile(`<[^>]+>`)

func stripHTML(s string) string {
	s = strings.ReplaceAll(s, "<br>", "\n")
	s = strings.ReplaceAll(s, "<br/>", "\n")
	s = strings.ReplaceAll(s, "<br />", "\n")
	s = strings.ReplaceAll(s, "<p>", "\n")
	s = htmlTagRe.ReplaceAllString(s, "")
	return strings.TrimSpace(s)
}

func humanSize(bytes int64) string {
	if bytes < 1024 {
		return fmt.Sprintf("%dB", bytes)
	}
	if bytes < 1024*1024 {
		return fmt.Sprintf("%.1fKB", float64(bytes)/1024)
	}
	return fmt.Sprintf("%.1fMB", float64(bytes)/(1024*1024))
}
