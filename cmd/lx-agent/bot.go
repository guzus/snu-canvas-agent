package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/mgnlia/lx-agent/internal/binding"
	"github.com/mgnlia/lx-agent/internal/canvas"
	"github.com/mgnlia/lx-agent/internal/monitor"
)

type telegramGetUpdatesResponse struct {
	OK     bool                `json:"ok"`
	Result []telegramBotUpdate `json:"result"`
}

type telegramBotUpdate struct {
	UpdateID      int64                     `json:"update_id"`
	Message       *telegramBotMessage       `json:"message"`
	CallbackQuery *telegramBotCallbackQuery `json:"callback_query"`
}

type telegramBotMessage struct {
	Text string `json:"text"`
	Chat struct {
		ID int64 `json:"id"`
	} `json:"chat"`
}

type telegramBotCallbackQuery struct {
	ID      string              `json:"id"`
	Data    string              `json:"data"`
	Message *telegramBotMessage `json:"message"`
}

type telegramInlineButton struct {
	Text         string `json:"text"`
	CallbackData string `json:"callback_data"`
}

type telegramInlineMarkup struct {
	InlineKeyboard [][]telegramInlineButton `json:"inline_keyboard"`
}

type botResponse struct {
	Text     string
	Keyboard *telegramInlineMarkup
}

type upcomingAssignment struct {
	CourseName string
	Item       canvas.Assignment
}

func handleBot(cfg config, client *canvas.Client, logger *slog.Logger) {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := runBotLoop(ctx, cfg, client, logger); err != nil && !errors.Is(err, context.Canceled) {
		exitErr(err)
	}
}

func handleServe(cfg config, client *canvas.Client, logger *slog.Logger) {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if strings.TrimSpace(cfg.Notifier.Telegram.BotToken) == "" {
		logger.Warn("serve mode without telegram bot token; running monitor only")
		if err := runMonitorService(ctx, cfg, client, logger); err != nil && !errors.Is(err, context.Canceled) {
			exitErr(err)
		}
		return
	}

	errCh := make(chan error, 2)
	go func() { errCh <- runMonitorService(ctx, cfg, client, logger) }()
	go func() { errCh <- runBotLoop(ctx, cfg, client, logger) }()

	for i := 0; i < 2; i++ {
		err := <-errCh
		if err != nil && !errors.Is(err, context.Canceled) {
			exitErr(err)
		}
	}
}

func runMonitorService(ctx context.Context, cfg config, client *canvas.Client, logger *slog.Logger) error {
	n := buildNotifier(ctx, cfg, logger)

	interval, err := time.ParseDuration(cfg.Monitor.PollInterval)
	if err != nil {
		return fmt.Errorf("invalid monitor.poll_interval: %w", err)
	}

	m := monitor.New(client, n, nil, monitor.Config{
		PollInterval:   interval,
		CourseFilter:   cfg.Monitor.Courses,
		SummarizeNew:   cfg.Monitor.SummarizeNew,
		DeadlineAlerts: cfg.Monitor.DeadlineAlerts,
		StatePath:      cfg.Monitor.StatePath,
	}, logger)

	return m.Run(ctx)
}

func runBotLoop(ctx context.Context, cfg config, client *canvas.Client, logger *slog.Logger) error {
	botToken := strings.TrimSpace(cfg.Notifier.Telegram.BotToken)
	if botToken == "" {
		return errors.New("bot mode requires TELEGRAM_BOT_TOKEN")
	}

	allowedChatID, err := resolveTelegramChatID(ctx, cfg)
	if err != nil {
		return fmt.Errorf("resolve bot chat id: %w", err)
	}
	if allowedChatID == "" {
		return errors.New("chat id not bound; run bind-chat first")
	}

	logger.Info("telegram command bot started", "chat_id", allowedChatID)

	var offset int64
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		updates, err := getTelegramUpdates(ctx, botToken, offset, 25)
		if err != nil {
			logger.Warn("getUpdates failed", "err", err)
			time.Sleep(3 * time.Second)
			continue
		}

		for _, u := range updates {
			if u.UpdateID >= offset {
				offset = u.UpdateID + 1
			}

			if u.CallbackQuery != nil {
				if u.CallbackQuery.Message == nil {
					_ = answerTelegramCallbackQuery(ctx, botToken, u.CallbackQuery.ID, "")
					continue
				}
				chatID := strconv.FormatInt(u.CallbackQuery.Message.Chat.ID, 10)
				if chatID != allowedChatID {
					_ = answerTelegramCallbackQuery(ctx, botToken, u.CallbackQuery.ID, msg("ko", "권한이 없습니다.", "Unauthorized."))
					continue
				}

				lang := languageForChat(ctx, cfg, chatID)
				resp := handleTelegramCallback(ctx, cfg, client, chatID, u.CallbackQuery.Data, lang)
				if strings.TrimSpace(resp.Text) != "" {
					if err := sendTelegramBotMessage(ctx, botToken, chatID, resp.Text, resp.Keyboard); err != nil {
						logger.Error("telegram callback reply failed", "err", err)
					}
				}
				_ = answerTelegramCallbackQuery(ctx, botToken, u.CallbackQuery.ID, "")
				continue
			}

			if u.Message == nil {
				continue
			}

			text := strings.TrimSpace(u.Message.Text)
			if text == "" {
				continue
			}

			chatID := strconv.FormatInt(u.Message.Chat.ID, 10)
			if chatID != allowedChatID {
				_ = sendTelegramBotMessage(ctx, botToken, chatID, "This bot is bound to a different chat.", nil)
				continue
			}

			lang := languageForChat(ctx, cfg, chatID)
			resp := handleTelegramCommand(ctx, cfg, client, chatID, text, lang)
			if strings.TrimSpace(resp.Text) == "" {
				continue
			}

			if err := sendTelegramBotMessage(ctx, botToken, chatID, resp.Text, resp.Keyboard); err != nil {
				logger.Error("telegram command reply failed", "err", err)
			}
		}
	}
}

func handleTelegramCommand(ctx context.Context, cfg config, client *canvas.Client, chatID, text, lang string) botResponse {
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return botResponse{}
	}

	cmd := strings.ToLower(fields[0])
	if i := strings.Index(cmd, "@"); i > 0 {
		cmd = cmd[:i]
	}
	args := fields[1:]

	switch cmd {
	case "/start", "/help":
		return botResponse{Text: botHelpMessage(lang)}
	case "/settings":
		return botResponse{Text: msg(lang, "언어를 선택하세요.", "Choose your language."), Keyboard: languageSettingsKeyboard(lang)}
	case "/status":
		filter := "all"
		if len(cfg.Monitor.Courses) > 0 {
			filter = strings.Trim(strings.Replace(fmt.Sprint(cfg.Monitor.Courses), " ", ",", -1), "[]")
		}
		if lang == "en" {
			return botResponse{Text: fmt.Sprintf("chat_id=%s\nmonitor_courses=%s\npoll_interval=%s\nlang=%s", chatID, filter, cfg.Monitor.PollInterval, lang)}
		}
		return botResponse{Text: fmt.Sprintf("chat_id=%s\nmonitor_courses=%s\npoll_interval=%s\n언어=%s", chatID, filter, cfg.Monitor.PollInterval, lang)}
	case "/bind":
		if strings.TrimSpace(cfg.Database.URL) == "" || strings.TrimSpace(cfg.Canvas.Token) == "" {
			return botResponse{Text: msg(lang, "바인딩에는 DATABASE_URL과 CANVAS_TOKEN이 필요합니다.", "Binding requires DATABASE_URL and CANVAS_TOKEN.")}
		}
		store, err := binding.New(cfg.Database.URL)
		if err != nil {
			return botResponse{Text: msg(lang, "DB 오류: ", "DB error: ") + err.Error()}
		}
		defer store.Close()
		if err := store.EnsureSchema(ctx); err != nil {
			return botResponse{Text: msg(lang, "DB 스키마 오류: ", "DB schema error: ") + err.Error()}
		}
		if err := store.Upsert(ctx, cfg.Canvas.Token, chatID); err != nil {
			return botResponse{Text: msg(lang, "DB 바인딩 오류: ", "DB bind error: ") + err.Error()}
		}
		return botResponse{Text: msg(lang, "현재 채팅을 Canvas 토큰에 바인딩했습니다.", "Bound this chat to the current Canvas token.")}
	case "/courses":
		return botResponse{Text: cmdCourses(ctx, cfg, client, args, lang)}
	case "/assignments":
		if len(args) > 0 {
			if _, err := strconv.Atoi(args[0]); err == nil {
				return botResponse{Text: msg(lang, "course_id 직접 입력은 더 이상 필요하지 않습니다. /assignments 를 눌러 강의를 선택하세요.", "Typing course_id is no longer needed. Use /assignments and pick a course.")}
			}
		}
		resp, err := cmdAssignmentsSelector(ctx, cfg, client, 10, lang)
		if err != nil {
			return botResponse{Text: msg(lang, "오류: ", "Error: ") + err.Error()}
		}
		return resp
	case "/upcoming":
		return botResponse{Text: cmdUpcoming(ctx, cfg, client, args, lang)}
	case "/announcements":
		return botResponse{Text: cmdAnnouncements(ctx, cfg, client, args, lang)}
	case "/files":
		if len(args) > 0 {
			if _, err := strconv.Atoi(args[0]); err == nil {
				return botResponse{Text: msg(lang, "course_id 직접 입력은 더 이상 필요하지 않습니다. /files 를 눌러 강의를 선택하세요.", "Typing course_id is no longer needed. Use /files and pick a course.")}
			}
		}
		resp, err := cmdFilesSelector(ctx, cfg, client, 10, lang)
		if err != nil {
			return botResponse{Text: msg(lang, "오류: ", "Error: ") + err.Error()}
		}
		return resp
	default:
		return botResponse{Text: msg(lang, "알 수 없는 명령어입니다. /help 를 사용하세요.", "Unknown command. Use /help.")}
	}
}

func handleTelegramCallback(ctx context.Context, cfg config, client *canvas.Client, chatID, data, lang string) botResponse {
	if strings.HasPrefix(data, "lang:") {
		target := strings.TrimSpace(strings.TrimPrefix(data, "lang:"))
		if target != "ko" && target != "en" {
			return botResponse{Text: msg(lang, "잘못된 언어 선택입니다.", "Invalid language selection.")}
		}
		if err := setChatLanguage(ctx, cfg, chatID, target); err != nil {
			return botResponse{Text: msg(lang, "언어 저장 실패: ", "Failed to save language: ") + err.Error()}
		}
		if target == "en" {
			return botResponse{Text: "Language set to English."}
		}
		return botResponse{Text: "언어가 한국어로 설정되었습니다."}
	}

	if strings.HasPrefix(data, "asg:") {
		parts := strings.Split(data, ":")
		if len(parts) != 3 {
			return botResponse{Text: msg(lang, "잘못된 선택 데이터입니다.", "Invalid selection payload.")}
		}
		courseID, err := strconv.Atoi(parts[1])
		if err != nil {
			return botResponse{Text: msg(lang, "강의 ID가 잘못되었습니다.", "Invalid course ID.")}
		}
		limit, err := strconv.Atoi(parts[2])
		if err != nil || limit <= 0 {
			limit = 10
		}
		return botResponse{Text: cmdAssignmentsByID(ctx, client, courseID, limit, lang)}
	}

	if strings.HasPrefix(data, "fil:") {
		parts := strings.Split(data, ":")
		if len(parts) != 3 {
			return botResponse{Text: msg(lang, "잘못된 선택 데이터입니다.", "Invalid selection payload.")}
		}
		courseID, err := strconv.Atoi(parts[1])
		if err != nil {
			return botResponse{Text: msg(lang, "강의 ID가 잘못되었습니다.", "Invalid course ID.")}
		}
		limit, err := strconv.Atoi(parts[2])
		if err != nil || limit <= 0 {
			limit = 10
		}
		return botResponse{Text: cmdFilesByID(ctx, client, courseID, limit, lang)}
	}

	return botResponse{Text: msg(lang, "지원하지 않는 동작입니다.", "Unsupported action.")}
}

func cmdAssignmentsSelector(ctx context.Context, cfg config, client *canvas.Client, limit int, lang string) (botResponse, error) {
	courses, err := monitoredCourses(ctx, cfg, client)
	if err != nil {
		return botResponse{}, err
	}
	if len(courses) == 0 {
		return botResponse{Text: msg(lang, "강의가 없습니다.", "No courses found.")}, nil
	}

	kb := &telegramInlineMarkup{}
	for _, c := range courses {
		label := c.Name
		if len([]rune(label)) > 48 {
			label = string([]rune(label)[:48]) + "..."
		}
		kb.InlineKeyboard = append(kb.InlineKeyboard, []telegramInlineButton{{
			Text:         label,
			CallbackData: fmt.Sprintf("asg:%d:%d", c.ID, limit),
		}})
	}

	return botResponse{Text: msg(lang, "과제를 볼 강의를 선택하세요.", "Select a course to view assignments."), Keyboard: kb}, nil
}

func cmdFilesSelector(ctx context.Context, cfg config, client *canvas.Client, limit int, lang string) (botResponse, error) {
	courses, err := monitoredCourses(ctx, cfg, client)
	if err != nil {
		return botResponse{}, err
	}
	if len(courses) == 0 {
		return botResponse{Text: msg(lang, "강의가 없습니다.", "No courses found.")}, nil
	}

	kb := &telegramInlineMarkup{}
	for _, c := range courses {
		label := c.Name
		if len([]rune(label)) > 48 {
			label = string([]rune(label)[:48]) + "..."
		}
		kb.InlineKeyboard = append(kb.InlineKeyboard, []telegramInlineButton{{
			Text:         label,
			CallbackData: fmt.Sprintf("fil:%d:%d", c.ID, limit),
		}})
	}

	return botResponse{Text: msg(lang, "파일을 볼 강의를 선택하세요.", "Select a course to view files."), Keyboard: kb}, nil
}

func cmdCourses(ctx context.Context, cfg config, client *canvas.Client, args []string, lang string) string {
	courses, err := monitoredCourses(ctx, cfg, client)
	if err != nil {
		return msg(lang, "오류: ", "Error: ") + err.Error()
	}
	if len(courses) == 0 {
		return msg(lang, "강의가 없습니다.", "No courses found.")
	}

	keyword := ""
	if len(args) > 0 {
		keyword = strings.ToLower(strings.Join(args, " "))
	}

	var lines []string
	lines = append(lines, msg(lang, "강의 목록:", "Courses:"))
	for _, c := range courses {
		if keyword != "" && !strings.Contains(strings.ToLower(c.Name), keyword) {
			continue
		}
		lines = append(lines, fmt.Sprintf("%d | %s", c.ID, c.Name))
	}

	if len(lines) == 1 {
		return msg(lang, "조건에 맞는 강의가 없습니다.", "No courses matched your filter.")
	}
	return trimForTelegram(strings.Join(lines, "\n"))
}

func cmdAssignments(ctx context.Context, client *canvas.Client, args []string, lang string) string {
	if len(args) == 0 {
		return msg(lang, "사용법: /assignments <course_id> [limit]", "Usage: /assignments <course_id> [limit]")
	}

	courseID, err := strconv.Atoi(args[0])
	if err != nil {
		return msg(lang, "잘못된 course_id 입니다.", "Invalid course_id.")
	}

	limit := 10
	if len(args) > 1 {
		if v, err := strconv.Atoi(args[1]); err == nil && v > 0 {
			limit = v
		}
	}

	return cmdAssignmentsByID(ctx, client, courseID, limit, lang)
}

func cmdAssignmentsByID(ctx context.Context, client *canvas.Client, courseID, limit int, lang string) string {
	if limit > 30 {
		limit = 30
	}
	if limit <= 0 {
		limit = 10
	}

	assignments, err := client.GetAssignments(ctx, courseID)
	if err != nil {
		return msg(lang, "오류: ", "Error: ") + err.Error()
	}
	if len(assignments) == 0 {
		return msg(lang, "과제가 없습니다.", "No assignments.")
	}

	sort.Slice(assignments, func(i, j int) bool {
		ai, aj := assignments[i].DueAt, assignments[j].DueAt
		if ai == nil && aj == nil {
			return assignments[i].CreatedAt.After(assignments[j].CreatedAt)
		}
		if ai == nil {
			return false
		}
		if aj == nil {
			return true
		}
		return ai.Before(*aj)
	})

	if len(assignments) > limit {
		assignments = assignments[:limit]
	}

	header := fmt.Sprintf("Assignments for %d:", courseID)
	if lang == "ko" {
		header = fmt.Sprintf("강의 %d 과제:", courseID)
	}
	lines := []string{header}
	for _, a := range assignments {
		due := msg(lang, "마감일 없음", "no due date")
		if a.DueAt != nil {
			due = a.DueAt.In(time.FixedZone("KST", 9*3600)).Format("2006-01-02 15:04 KST")
		}
		if lang == "ko" {
			lines = append(lines, fmt.Sprintf("- %s | %s | %.0f점", due, a.Name, a.PointsPossible))
		} else {
			lines = append(lines, fmt.Sprintf("- %s | %s | %.0f pts", due, a.Name, a.PointsPossible))
		}
	}
	return trimForTelegram(strings.Join(lines, "\n"))
}

func cmdUpcoming(ctx context.Context, cfg config, client *canvas.Client, args []string, lang string) string {
	days := 14
	limit := 20
	if len(args) > 0 {
		if v, err := strconv.Atoi(args[0]); err == nil && v > 0 {
			days = v
		}
	}
	if len(args) > 1 {
		if v, err := strconv.Atoi(args[1]); err == nil && v > 0 {
			limit = v
		}
	}
	if days > 90 {
		days = 90
	}
	if limit > 50 {
		limit = 50
	}

	courses, err := monitoredCourses(ctx, cfg, client)
	if err != nil {
		return msg(lang, "오류: ", "Error: ") + err.Error()
	}

	now := time.Now()
	until := now.Add(time.Duration(days) * 24 * time.Hour)
	var upcoming []upcomingAssignment

	for _, c := range courses {
		assignments, err := client.GetAssignments(ctx, c.ID)
		if err != nil {
			continue
		}
		for _, a := range assignments {
			if a.DueAt == nil || a.Submitted {
				continue
			}
			if a.DueAt.After(now) && a.DueAt.Before(until) {
				upcoming = append(upcoming, upcomingAssignment{CourseName: c.Name, Item: a})
			}
		}
	}

	sort.Slice(upcoming, func(i, j int) bool {
		return upcoming[i].Item.DueAt.Before(*upcoming[j].Item.DueAt)
	})

	if len(upcoming) == 0 {
		if lang == "ko" {
			return fmt.Sprintf("향후 %d일 이내 마감 과제가 없습니다.", days)
		}
		return fmt.Sprintf("No upcoming assignments in the next %d days.", days)
	}
	if len(upcoming) > limit {
		upcoming = upcoming[:limit]
	}

	header := fmt.Sprintf("Upcoming assignments (%dd):", days)
	if lang == "ko" {
		header = fmt.Sprintf("다가오는 과제 (%d일):", days)
	}
	lines := []string{header}
	for _, item := range upcoming {
		due := item.Item.DueAt.In(time.FixedZone("KST", 9*3600)).Format("2006-01-02 15:04 KST")
		lines = append(lines, fmt.Sprintf("- %s | %s | %s", due, item.CourseName, item.Item.Name))
	}
	return trimForTelegram(strings.Join(lines, "\n"))
}

func cmdAnnouncements(ctx context.Context, cfg config, client *canvas.Client, args []string, lang string) string {
	limit := 10
	if len(args) > 0 {
		if v, err := strconv.Atoi(args[0]); err == nil && v > 0 {
			limit = v
		}
	}
	if limit > 30 {
		limit = 30
	}

	courses, err := monitoredCourses(ctx, cfg, client)
	if err != nil {
		return msg(lang, "오류: ", "Error: ") + err.Error()
	}
	if len(courses) == 0 {
		return msg(lang, "강의가 없습니다.", "No courses found.")
	}

	courseName := make(map[string]string, len(courses))
	ids := make([]int, 0, len(courses))
	for _, c := range courses {
		courseName[fmt.Sprintf("course_%d", c.ID)] = c.Name
		ids = append(ids, c.ID)
	}

	anns, err := client.GetAnnouncements(ctx, ids)
	if err != nil {
		return msg(lang, "오류: ", "Error: ") + err.Error()
	}
	if len(anns) == 0 {
		return msg(lang, "공지가 없습니다.", "No announcements.")
	}

	sort.Slice(anns, func(i, j int) bool {
		return anns[i].PostedAt.After(anns[j].PostedAt)
	})
	if len(anns) > limit {
		anns = anns[:limit]
	}

	header := msg(lang, "최근 공지:", "Recent announcements:")
	lines := []string{header}
	for _, a := range anns {
		name := courseName[a.ContextCode]
		if name == "" {
			name = a.ContextCode
		}
		posted := a.PostedAt.In(time.FixedZone("KST", 9*3600)).Format("2006-01-02")
		lines = append(lines, fmt.Sprintf("- %s | %s | %s", posted, name, a.Title))
	}
	return trimForTelegram(strings.Join(lines, "\n"))
}

func cmdFilesByID(ctx context.Context, client *canvas.Client, courseID, limit int, lang string) string {
	if limit > 30 {
		limit = 30
	}
	if limit <= 0 {
		limit = 10
	}

	files, err := client.GetFiles(ctx, courseID)
	if err != nil {
		return msg(lang, "오류: ", "Error: ") + err.Error()
	}
	if len(files) == 0 {
		return msg(lang, "파일이 없습니다.", "No files.")
	}
	if len(files) > limit {
		files = files[:limit]
	}

	header := fmt.Sprintf("Recent files for %d:", courseID)
	if lang == "ko" {
		header = fmt.Sprintf("강의 %d 최근 파일:", courseID)
	}
	lines := []string{header}
	for _, f := range files {
		updated := f.UpdatedAt.In(time.FixedZone("KST", 9*3600)).Format("2006-01-02")
		lines = append(lines, fmt.Sprintf("- %s | %s | %d bytes", updated, f.DisplayName, f.Size))
	}
	return trimForTelegram(strings.Join(lines, "\n"))
}

func monitoredCourses(ctx context.Context, cfg config, client *canvas.Client) ([]canvas.Course, error) {
	courses, err := client.GetCourses(ctx)
	if err != nil {
		return nil, err
	}
	if len(cfg.Monitor.Courses) == 0 {
		return courses, nil
	}

	filter := make(map[int]bool, len(cfg.Monitor.Courses))
	for _, id := range cfg.Monitor.Courses {
		filter[id] = true
	}

	var out []canvas.Course
	for _, c := range courses {
		if filter[c.ID] {
			out = append(out, c)
		}
	}
	return out, nil
}

func botHelpMessage(lang string) string {
	if lang == "en" {
		return strings.Join([]string{
			"Available commands:",
			"/status",
			"/settings",
			"/courses [keyword]",
			"/assignments (interactive course selector)",
			"/upcoming [days] [limit]",
			"/announcements [limit]",
			"/files (interactive course selector)",
			"/bind",
		}, "\n")
	}
	return strings.Join([]string{
		"사용 가능한 명령어:",
		"/status",
		"/settings",
		"/courses [키워드]",
		"/assignments (강의 선택기)",
		"/upcoming [days] [limit]",
		"/announcements [limit]",
		"/files (강의 선택기)",
		"/bind",
	}, "\n")
}

func languageSettingsKeyboard(lang string) *telegramInlineMarkup {
	return &telegramInlineMarkup{InlineKeyboard: [][]telegramInlineButton{
		{
			{Text: langLabel(lang, "한국어", "Korean"), CallbackData: "lang:ko"},
			{Text: langLabel(lang, "English", "English"), CallbackData: "lang:en"},
		},
	}}
}

func langLabel(lang, ko, en string) string {
	if lang == "en" {
		return en
	}
	return ko
}

func msg(lang, ko, en string) string {
	if lang == "en" {
		return en
	}
	return ko
}

func languageForChat(ctx context.Context, cfg config, chatID string) string {
	if strings.TrimSpace(cfg.Database.URL) == "" {
		return "ko"
	}

	store, err := binding.New(cfg.Database.URL)
	if err != nil {
		return "ko"
	}
	defer store.Close()

	if err := store.EnsureSchema(ctx); err != nil {
		return "ko"
	}

	lang, err := store.GetChatLanguage(ctx, chatID)
	if err != nil || lang == "" {
		return "ko"
	}
	return lang
}

func setChatLanguage(ctx context.Context, cfg config, chatID, lang string) error {
	if strings.TrimSpace(cfg.Database.URL) == "" {
		return errors.New("DATABASE_URL is required")
	}

	store, err := binding.New(cfg.Database.URL)
	if err != nil {
		return err
	}
	defer store.Close()

	if err := store.EnsureSchema(ctx); err != nil {
		return err
	}
	return store.SetChatLanguage(ctx, chatID, lang)
}

func sendTelegramBotMessage(ctx context.Context, botToken, chatID, message string, keyboard *telegramInlineMarkup) error {
	u := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", botToken)
	params := url.Values{
		"chat_id": {chatID},
		"text":    {trimForTelegram(message)},
	}
	if keyboard != nil {
		b, _ := json.Marshal(keyboard)
		params.Set("reply_markup", string(b))
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, strings.NewReader(params.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("telegram send: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("telegram error: %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

func answerTelegramCallbackQuery(ctx context.Context, botToken, callbackQueryID, text string) error {
	u := fmt.Sprintf("https://api.telegram.org/bot%s/answerCallbackQuery", botToken)
	params := url.Values{"callback_query_id": {callbackQueryID}}
	if strings.TrimSpace(text) != "" {
		params.Set("text", text)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, strings.NewReader(params.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("answerCallbackQuery error: %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

func trimForTelegram(message string) string {
	const maxChars = 3800
	r := []rune(message)
	if len(r) <= maxChars {
		return message
	}
	return string(r[:maxChars]) + "\n...[truncated]"
}

func getTelegramUpdates(ctx context.Context, botToken string, offset int64, timeoutSec int) ([]telegramBotUpdate, error) {
	params := url.Values{
		"offset":  {strconv.FormatInt(offset, 10)},
		"timeout": {strconv.Itoa(timeoutSec)},
	}
	apiURL := fmt.Sprintf("https://api.telegram.org/bot%s/getUpdates?%s", botToken, params.Encode())

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build getUpdates request: %w", err)
	}

	httpClient := &http.Client{Timeout: time.Duration(timeoutSec+10) * time.Second}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("getUpdates request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("getUpdates error %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var parsed telegramGetUpdatesResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("decode getUpdates response: %w", err)
	}
	if !parsed.OK {
		return nil, errors.New("getUpdates returned ok=false")
	}
	return parsed.Result, nil
}
