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
	"github.com/mgnlia/lx-agent/internal/notifier"
)

type telegramGetUpdatesResponse struct {
	OK     bool                `json:"ok"`
	Result []telegramBotUpdate `json:"result"`
}

type telegramBotUpdate struct {
	UpdateID int64               `json:"update_id"`
	Message  *telegramBotMessage `json:"message"`
}

type telegramBotMessage struct {
	Text string `json:"text"`
	Chat struct {
		ID int64 `json:"id"`
	} `json:"chat"`
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
	s := buildSummarizer(cfg)

	interval, err := time.ParseDuration(cfg.Monitor.PollInterval)
	if err != nil {
		return fmt.Errorf("invalid monitor.poll_interval: %w", err)
	}

	m := monitor.New(client, n, s, monitor.Config{
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
			if u.Message == nil {
				continue
			}

			text := strings.TrimSpace(u.Message.Text)
			if text == "" {
				continue
			}

			chatID := strconv.FormatInt(u.Message.Chat.ID, 10)
			if chatID != allowedChatID {
				_ = sendTelegramBotMessage(ctx, botToken, chatID, "This bot is bound to a different chat.")
				continue
			}

			reply := handleTelegramCommand(ctx, cfg, client, chatID, text)
			if strings.TrimSpace(reply) == "" {
				continue
			}

			if err := sendTelegramBotMessage(ctx, botToken, chatID, reply); err != nil {
				logger.Error("telegram command reply failed", "err", err)
			}
		}
	}
}

func handleTelegramCommand(ctx context.Context, cfg config, client *canvas.Client, chatID, text string) string {
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return ""
	}

	cmd := strings.ToLower(fields[0])
	if i := strings.Index(cmd, "@"); i > 0 {
		cmd = cmd[:i]
	}
	args := fields[1:]

	switch cmd {
	case "/start", "/help":
		return botHelpMessage()
	case "/status":
		filter := "all"
		if len(cfg.Monitor.Courses) > 0 {
			filter = strings.Trim(strings.Replace(fmt.Sprint(cfg.Monitor.Courses), " ", ",", -1), "[]")
		}
		return fmt.Sprintf("chat_id=%s\nmonitor_courses=%s\npoll_interval=%s", chatID, filter, cfg.Monitor.PollInterval)
	case "/bind":
		if strings.TrimSpace(cfg.Database.URL) == "" || strings.TrimSpace(cfg.Canvas.Token) == "" {
			return "Binding requires DATABASE_URL and CANVAS_TOKEN."
		}
		store, err := binding.New(cfg.Database.URL)
		if err != nil {
			return "DB error: " + err.Error()
		}
		defer store.Close()
		if err := store.EnsureSchema(ctx); err != nil {
			return "DB schema error: " + err.Error()
		}
		if err := store.Upsert(ctx, cfg.Canvas.Token, chatID); err != nil {
			return "DB bind error: " + err.Error()
		}
		return "Bound this chat to the current Canvas token."
	case "/courses":
		return cmdCourses(ctx, cfg, client, args)
	case "/assignments":
		return cmdAssignments(ctx, client, args)
	case "/upcoming":
		return cmdUpcoming(ctx, cfg, client, args)
	case "/announcements":
		return cmdAnnouncements(ctx, cfg, client, args)
	case "/files":
		return cmdFiles(ctx, client, args)
	default:
		return "Unknown command. Use /help."
	}
}

func cmdCourses(ctx context.Context, cfg config, client *canvas.Client, args []string) string {
	courses, err := monitoredCourses(ctx, cfg, client)
	if err != nil {
		return "Error: " + err.Error()
	}
	if len(courses) == 0 {
		return "No courses found."
	}

	keyword := ""
	if len(args) > 0 {
		keyword = strings.ToLower(strings.Join(args, " "))
	}

	var lines []string
	lines = append(lines, "Courses:")
	for _, c := range courses {
		if keyword != "" && !strings.Contains(strings.ToLower(c.Name), keyword) {
			continue
		}
		lines = append(lines, fmt.Sprintf("%d | %s", c.ID, c.Name))
	}

	if len(lines) == 1 {
		return "No courses matched your filter."
	}
	return trimForTelegram(strings.Join(lines, "\n"))
}

func cmdAssignments(ctx context.Context, client *canvas.Client, args []string) string {
	if len(args) == 0 {
		return "Usage: /assignments <course_id> [limit]"
	}
	courseID, err := strconv.Atoi(args[0])
	if err != nil {
		return "Invalid course_id."
	}

	limit := 10
	if len(args) > 1 {
		if v, err := strconv.Atoi(args[1]); err == nil && v > 0 {
			limit = v
		}
	}
	if limit > 30 {
		limit = 30
	}

	assignments, err := client.GetAssignments(ctx, courseID)
	if err != nil {
		return "Error: " + err.Error()
	}
	if len(assignments) == 0 {
		return "No assignments."
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

	lines := []string{fmt.Sprintf("Assignments for %d:", courseID)}
	for _, a := range assignments {
		due := "no due date"
		if a.DueAt != nil {
			due = a.DueAt.In(time.FixedZone("KST", 9*3600)).Format("2006-01-02 15:04 KST")
		}
		lines = append(lines, fmt.Sprintf("- %s | %s | %.0f pts", due, a.Name, a.PointsPossible))
	}
	return trimForTelegram(strings.Join(lines, "\n"))
}

func cmdUpcoming(ctx context.Context, cfg config, client *canvas.Client, args []string) string {
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
		return "Error: " + err.Error()
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
		return fmt.Sprintf("No upcoming assignments in the next %d days.", days)
	}
	if len(upcoming) > limit {
		upcoming = upcoming[:limit]
	}

	lines := []string{fmt.Sprintf("Upcoming assignments (%dd):", days)}
	for _, item := range upcoming {
		due := item.Item.DueAt.In(time.FixedZone("KST", 9*3600)).Format("2006-01-02 15:04 KST")
		lines = append(lines, fmt.Sprintf("- %s | %s | %s", due, item.CourseName, item.Item.Name))
	}
	return trimForTelegram(strings.Join(lines, "\n"))
}

func cmdAnnouncements(ctx context.Context, cfg config, client *canvas.Client, args []string) string {
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
		return "Error: " + err.Error()
	}
	if len(courses) == 0 {
		return "No courses found."
	}

	courseName := make(map[string]string, len(courses))
	ids := make([]int, 0, len(courses))
	for _, c := range courses {
		courseName[fmt.Sprintf("course_%d", c.ID)] = c.Name
		ids = append(ids, c.ID)
	}

	anns, err := client.GetAnnouncements(ctx, ids)
	if err != nil {
		return "Error: " + err.Error()
	}
	if len(anns) == 0 {
		return "No announcements."
	}

	sort.Slice(anns, func(i, j int) bool {
		return anns[i].PostedAt.After(anns[j].PostedAt)
	})
	if len(anns) > limit {
		anns = anns[:limit]
	}

	lines := []string{"Recent announcements:"}
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

func cmdFiles(ctx context.Context, client *canvas.Client, args []string) string {
	if len(args) == 0 {
		return "Usage: /files <course_id> [limit]"
	}
	courseID, err := strconv.Atoi(args[0])
	if err != nil {
		return "Invalid course_id."
	}

	limit := 10
	if len(args) > 1 {
		if v, err := strconv.Atoi(args[1]); err == nil && v > 0 {
			limit = v
		}
	}
	if limit > 30 {
		limit = 30
	}

	files, err := client.GetFiles(ctx, courseID)
	if err != nil {
		return "Error: " + err.Error()
	}
	if len(files) == 0 {
		return "No files."
	}
	if len(files) > limit {
		files = files[:limit]
	}

	lines := []string{fmt.Sprintf("Recent files for %d:", courseID)}
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

func botHelpMessage() string {
	return strings.Join([]string{
		"Available commands:",
		"/status",
		"/courses [keyword]",
		"/assignments <course_id> [limit]",
		"/upcoming [days] [limit]",
		"/announcements [limit]",
		"/files <course_id> [limit]",
		"/bind",
	}, "\n")
}

func sendTelegramBotMessage(ctx context.Context, botToken, chatID, message string) error {
	return notifier.NewTelegram(botToken, chatID).Send(ctx, trimForTelegram(message))
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
