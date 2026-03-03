package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/mgnlia/lx-agent/internal/binding"
	"github.com/mgnlia/lx-agent/internal/canvas"
	"github.com/mgnlia/lx-agent/internal/monitor"
	"github.com/mgnlia/lx-agent/internal/notifier"
	"github.com/mgnlia/lx-agent/internal/summarizer"
)

type config struct {
	Canvas struct {
		URL   string `yaml:"url"`
		Token string `yaml:"token"`
	} `yaml:"canvas"`
	Monitor struct {
		PollInterval   string `yaml:"poll_interval"`
		SummarizeNew   bool   `yaml:"summarize_new"`
		DeadlineAlerts []int  `yaml:"deadline_alerts"`
		StatePath      string `yaml:"state_path"`
		Courses        []int  `yaml:"courses"`
	} `yaml:"monitor"`
	Summarizer struct {
		Provider     string `yaml:"provider"`
		GeminiAPIKey string `yaml:"gemini_api_key"`
	} `yaml:"summarizer"`
	Notifier struct {
		Provider string `yaml:"provider"`
		Telegram struct {
			BotToken string `yaml:"bot_token"`
			ChatID   string `yaml:"chat_id"`
		} `yaml:"telegram"`
	} `yaml:"notifier"`
	Database struct {
		URL string `yaml:"url"`
	} `yaml:"database"`
}

type assignmentRow struct {
	CourseName string
	Item       canvas.Assignment
}

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

	cfgPath, args, err := parseGlobalArgs(os.Args[1:])
	if err != nil {
		exitErr(err)
	}

	cfg, err := loadConfig(cfgPath)
	if err != nil {
		exitErr(err)
	}
	applyEnvOverrides(&cfg)
	applyDefaults(&cfg)

	if len(args) == 0 {
		printUsage()
		return
	}

	cmd := args[0]
	cmdArgs := args[1:]

	if cmd == "config" {
		printConfig(cfg)
		return
	}

	if cmd == "bind-chat" {
		handleBindChat(context.Background(), cfg, cmdArgs)
		return
	}

	if cfg.Canvas.URL == "" || cfg.Canvas.Token == "" {
		exitErr(errors.New("canvas.url and canvas.token are required (config or env: CANVAS_URL/CANVAS_TOKEN)"))
	}

	ctx := context.Background()
	client := canvas.NewClient(cfg.Canvas.URL, cfg.Canvas.Token, logger)

	switch cmd {
	case "courses":
		handleCourses(ctx, client)
	case "assignments":
		handleAssignments(ctx, client, cmdArgs)
	case "files":
		handleFiles(ctx, client, cmdArgs)
	case "announcements":
		handleAnnouncements(ctx, client)
	case "once", "run":
		handleMonitor(cmd, cfg, client, logger)
	default:
		exitErr(fmt.Errorf("unknown command: %s", cmd))
	}
}

func handleCourses(ctx context.Context, client *canvas.Client) {
	courses, err := client.GetCourses(ctx)
	if err != nil {
		exitErr(err)
	}
	if len(courses) == 0 {
		fmt.Println("No active courses")
		return
	}
	for _, c := range courses {
		fmt.Printf("%d\t%s\n", c.ID, c.Name)
	}
}

func handleAssignments(ctx context.Context, client *canvas.Client, args []string) {
	courseID, hasID := parseOptionalCourseID(args)
	if hasID {
		as, err := client.GetAssignments(ctx, courseID)
		if err != nil {
			exitErr(err)
		}
		courseName := strconv.Itoa(courseID)
		if course, err := client.GetCourse(ctx, courseID); err == nil && course != nil && course.Name != "" {
			courseName = course.Name
		}
		printAssignments(courseName, as)
		return
	}

	courses, err := client.GetCourses(ctx)
	if err != nil {
		exitErr(err)
	}

	var rows []assignmentRow
	for _, c := range courses {
		as, err := client.GetAssignments(ctx, c.ID)
		if err != nil {
			continue
		}
		for _, a := range as {
			rows = append(rows, assignmentRow{CourseName: c.Name, Item: a})
		}
	}

	sort.Slice(rows, func(i, j int) bool {
		ai, aj := rows[i].Item.DueAt, rows[j].Item.DueAt
		if ai == nil && aj == nil {
			return rows[i].Item.CreatedAt.After(rows[j].Item.CreatedAt)
		}
		if ai == nil {
			return false
		}
		if aj == nil {
			return true
		}
		return ai.Before(*aj)
	})

	if len(rows) == 0 {
		fmt.Println("No assignments")
		return
	}

	for _, r := range rows {
		due := "none"
		if r.Item.DueAt != nil {
			due = r.Item.DueAt.Format(time.RFC3339)
		}
		fmt.Printf("[%s]\t%d\t%s\tdue=%s\tpoints=%.0f\n", r.CourseName, r.Item.ID, r.Item.Name, due, r.Item.PointsPossible)
	}
}

func handleFiles(ctx context.Context, client *canvas.Client, args []string) {
	courseID, hasID := parseOptionalCourseID(args)
	if hasID {
		files, err := client.GetFiles(ctx, courseID)
		if err != nil {
			exitErr(err)
		}
		printFiles(strconv.Itoa(courseID), files)
		return
	}

	courses, err := client.GetCourses(ctx)
	if err != nil {
		exitErr(err)
	}
	for _, c := range courses {
		files, err := client.GetFiles(ctx, c.ID)
		if err != nil {
			continue
		}
		printFiles(c.Name, files)
	}
}

func handleAnnouncements(ctx context.Context, client *canvas.Client) {
	courses, err := client.GetCourses(ctx)
	if err != nil {
		exitErr(err)
	}
	if len(courses) == 0 {
		fmt.Println("No active courses")
		return
	}

	ids := make([]int, 0, len(courses))
	courseName := make(map[string]string, len(courses))
	for _, c := range courses {
		ids = append(ids, c.ID)
		courseName[fmt.Sprintf("course_%d", c.ID)] = c.Name
	}

	anns, err := client.GetAnnouncements(ctx, ids)
	if err != nil {
		exitErr(err)
	}
	if len(anns) == 0 {
		fmt.Println("No announcements")
		return
	}

	sort.Slice(anns, func(i, j int) bool {
		return anns[i].PostedAt.After(anns[j].PostedAt)
	})

	for _, a := range anns {
		name := courseName[a.ContextCode]
		if name == "" {
			name = a.ContextCode
		}
		fmt.Printf("[%s]\t%s\t%s\n", name, a.PostedAt.Format(time.RFC3339), a.Title)
	}
}

func handleBindChat(ctx context.Context, cfg config, args []string) {
	if strings.TrimSpace(cfg.Canvas.Token) == "" {
		exitErr(errors.New("bind-chat requires canvas.token (or CANVAS_TOKEN)"))
	}
	if strings.TrimSpace(cfg.Database.URL) == "" {
		exitErr(errors.New("bind-chat requires database.url (or DATABASE_URL)"))
	}

	var chatID string
	if len(args) > 0 {
		chatID = strings.TrimSpace(args[0])
	} else {
		if strings.TrimSpace(cfg.Notifier.Telegram.BotToken) == "" {
			exitErr(errors.New("bind-chat without a chat-id requires telegram bot token (notifier.telegram.bot_token or TELEGRAM_BOT_TOKEN)"))
		}
		id, err := fetchLatestTelegramChatID(ctx, cfg.Notifier.Telegram.BotToken)
		if err != nil {
			exitErr(err)
		}
		chatID = id
	}
	if chatID == "" {
		exitErr(errors.New("empty chat-id"))
	}

	store, err := binding.New(cfg.Database.URL)
	if err != nil {
		exitErr(err)
	}
	defer store.Close()

	if err := store.EnsureSchema(ctx); err != nil {
		exitErr(err)
	}
	if err := store.Upsert(ctx, cfg.Canvas.Token, chatID); err != nil {
		exitErr(err)
	}

	fmt.Printf("bound canvas token to chat id: %s\n", chatID)
}

func handleMonitor(command string, cfg config, client *canvas.Client, logger *slog.Logger) {
	n := buildNotifier(context.Background(), cfg, logger)
	s := buildSummarizer(cfg)

	interval, err := time.ParseDuration(cfg.Monitor.PollInterval)
	if err != nil {
		exitErr(fmt.Errorf("invalid monitor.poll_interval: %w", err))
	}

	m := monitor.New(client, n, s, monitor.Config{
		PollInterval:   interval,
		CourseFilter:   cfg.Monitor.Courses,
		SummarizeNew:   cfg.Monitor.SummarizeNew,
		DeadlineAlerts: cfg.Monitor.DeadlineAlerts,
		StatePath:      cfg.Monitor.StatePath,
	}, logger)

	if command == "once" {
		if err := m.RunOnce(context.Background()); err != nil {
			exitErr(err)
		}
		return
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := m.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		exitErr(err)
	}
}

func buildNotifier(ctx context.Context, cfg config, logger *slog.Logger) notifier.Notifier {
	switch strings.ToLower(strings.TrimSpace(cfg.Notifier.Provider)) {
	case "", "stdout":
		return notifier.NewStdout()
	case "telegram":
		if cfg.Notifier.Telegram.BotToken == "" {
			exitErr(errors.New("telegram provider requires notifier.telegram.bot_token"))
		}
		chatID, err := resolveTelegramChatID(ctx, cfg)
		if err != nil {
			logger.Warn("telegram chat resolution failed", "err", err)
		}
		if chatID == "" {
			logger.Warn("telegram chat id unresolved; falling back to stdout")
			return notifier.NewStdout()
		}

		if strings.TrimSpace(cfg.Database.URL) != "" && strings.TrimSpace(cfg.Canvas.Token) != "" {
			store, err := binding.New(cfg.Database.URL)
			if err == nil {
				defer store.Close()
				if err := store.EnsureSchema(ctx); err == nil {
					_ = store.Upsert(ctx, cfg.Canvas.Token, chatID)
				}
			}
		}

		return notifier.NewTelegram(cfg.Notifier.Telegram.BotToken, chatID)
	default:
		exitErr(fmt.Errorf("unsupported notifier provider: %s", cfg.Notifier.Provider))
		return nil
	}
}

func buildSummarizer(cfg config) summarizer.Summarizer {
	provider := strings.ToLower(strings.TrimSpace(cfg.Summarizer.Provider))
	if provider == "gemini" && strings.TrimSpace(cfg.Summarizer.GeminiAPIKey) != "" {
		return summarizer.NewGemini(cfg.Summarizer.GeminiAPIKey)
	}
	return nil
}

func resolveTelegramChatID(ctx context.Context, cfg config) (string, error) {
	chatID := strings.TrimSpace(cfg.Notifier.Telegram.ChatID)
	if chatID != "" {
		return chatID, nil
	}
	if strings.TrimSpace(cfg.Database.URL) == "" || strings.TrimSpace(cfg.Canvas.Token) == "" {
		return "", nil
	}

	store, err := binding.New(cfg.Database.URL)
	if err != nil {
		return "", err
	}
	defer store.Close()

	if err := store.EnsureSchema(ctx); err != nil {
		return "", err
	}
	return store.LookupChatID(ctx, cfg.Canvas.Token)
}

type telegramUpdatesResponse struct {
	OK     bool `json:"ok"`
	Result []struct {
		Message *struct {
			Chat struct {
				ID int64 `json:"id"`
			} `json:"chat"`
		} `json:"message"`
	} `json:"result"`
}

func fetchLatestTelegramChatID(ctx context.Context, botToken string) (string, error) {
	url := fmt.Sprintf("https://api.telegram.org/bot%s/getUpdates", botToken)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("build getUpdates request: %w", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("getUpdates request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read getUpdates response: %w", err)
	}
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("telegram getUpdates error %d", resp.StatusCode)
	}

	var parsed telegramUpdatesResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", fmt.Errorf("decode getUpdates response: %w", err)
	}
	if !parsed.OK {
		return "", errors.New("telegram getUpdates returned ok=false")
	}

	for i := len(parsed.Result) - 1; i >= 0; i-- {
		msg := parsed.Result[i].Message
		if msg == nil {
			continue
		}
		return strconv.FormatInt(msg.Chat.ID, 10), nil
	}
	return "", errors.New("no chat id found; send /start to the bot from your target chat, then run bind-chat again")
}

func parseGlobalArgs(args []string) (string, []string, error) {
	cfgPath := "config.yaml"
	for len(args) >= 2 {
		if args[0] != "-config" && args[0] != "--config" {
			break
		}
		cfgPath = args[1]
		args = args[2:]
	}
	if len(args) == 0 {
		return cfgPath, args, nil
	}
	return cfgPath, args, nil
}

func parseOptionalCourseID(args []string) (int, bool) {
	if len(args) == 0 {
		return 0, false
	}
	id, err := strconv.Atoi(args[0])
	if err != nil {
		exitErr(fmt.Errorf("invalid course-id: %q", args[0]))
	}
	return id, true
}

func loadConfig(path string) (config, error) {
	var cfg config
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return cfg, fmt.Errorf("read config: %w", err)
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("parse config: %w", err)
	}
	return cfg, nil
}

func applyEnvOverrides(cfg *config) {
	if v := strings.TrimSpace(os.Getenv("CANVAS_URL")); v != "" {
		cfg.Canvas.URL = v
	}
	if v := strings.TrimSpace(os.Getenv("CANVAS_TOKEN")); v != "" {
		cfg.Canvas.Token = v
	}
	if v := strings.TrimSpace(os.Getenv("GEMINI_API_KEY")); v != "" {
		cfg.Summarizer.GeminiAPIKey = v
		if cfg.Summarizer.Provider == "" {
			cfg.Summarizer.Provider = "gemini"
		}
	}
	if v := strings.TrimSpace(os.Getenv("TELEGRAM_BOT_TOKEN")); v != "" {
		cfg.Notifier.Telegram.BotToken = v
		cfg.Notifier.Provider = "telegram"
	}
	if v := strings.TrimSpace(os.Getenv("TELEGRAM_CHAT_ID")); v != "" {
		cfg.Notifier.Telegram.ChatID = v
		cfg.Notifier.Provider = "telegram"
	}
	if v := strings.TrimSpace(os.Getenv("DATABASE_URL")); v != "" {
		cfg.Database.URL = v
	}
}

func applyDefaults(cfg *config) {
	if cfg.Monitor.PollInterval == "" {
		cfg.Monitor.PollInterval = "10m"
	}
	if len(cfg.Monitor.DeadlineAlerts) == 0 {
		cfg.Monitor.DeadlineAlerts = []int{3, 1, 0}
	}
	if cfg.Monitor.StatePath == "" {
		cfg.Monitor.StatePath = "lx-state.json"
	}
	if cfg.Notifier.Provider == "" {
		cfg.Notifier.Provider = "stdout"
	}
}

func printAssignments(courseName string, assignments []canvas.Assignment) {
	if len(assignments) == 0 {
		fmt.Printf("No assignments for %s\n", courseName)
		return
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

	for _, a := range assignments {
		due := "none"
		if a.DueAt != nil {
			due = a.DueAt.Format(time.RFC3339)
		}
		fmt.Printf("[%s]\t%d\t%s\tdue=%s\tpoints=%.0f\n", courseName, a.ID, a.Name, due, a.PointsPossible)
	}
}

func printFiles(courseName string, files []canvas.File) {
	if len(files) == 0 {
		return
	}
	fmt.Printf("[%s]\n", courseName)
	for _, f := range files {
		fmt.Printf("  %d\t%s\t%d bytes\tupdated=%s\n", f.ID, f.DisplayName, f.Size, f.UpdatedAt.Format(time.RFC3339))
	}
}

func printConfig(cfg config) {
	masked := cfg
	if masked.Canvas.Token != "" {
		masked.Canvas.Token = maskSecret(masked.Canvas.Token)
	}
	if masked.Summarizer.GeminiAPIKey != "" {
		masked.Summarizer.GeminiAPIKey = maskSecret(masked.Summarizer.GeminiAPIKey)
	}
	if masked.Notifier.Telegram.BotToken != "" {
		masked.Notifier.Telegram.BotToken = maskSecret(masked.Notifier.Telegram.BotToken)
	}
	if masked.Database.URL != "" {
		masked.Database.URL = maskSecret(masked.Database.URL)
	}

	data, err := yaml.Marshal(masked)
	if err != nil {
		exitErr(err)
	}
	fmt.Print(string(data))
}

func maskSecret(v string) string {
	if len(v) <= 8 {
		return "****"
	}
	return v[:4] + "..." + v[len(v)-4:]
}

func printUsage() {
	fmt.Println(`Usage:
  lx-agent [-config path] <command> [args]

Commands:
  courses
  assignments [course-id]
  files [course-id]
  announcements
  bind-chat [chat-id]
  once
  run
  config`)
}

func exitErr(err error) {
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(1)
}
