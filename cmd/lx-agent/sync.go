package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/mgnlia/lx-agent/internal/archive"
	"github.com/mgnlia/lx-agent/internal/canvas"
	"github.com/mgnlia/lx-agent/internal/summarizer"
)

// handleSync mirrors every course file to local disk. It is the command the
// scheduled job runs, so it is written to fail loudly: an expired credential
// exits non-zero and notifies, rather than reporting a successful run that
// archived nothing.
func handleSync(ctx context.Context, cfg config, client *canvas.Client, logger *slog.Logger, args []string) {
	opts := archive.Options{
		Dir:           cfg.Archive.Dir,
		Courses:       cfg.Archive.Courses,
		IncludeVideos: cfg.Archive.IncludeVideos,
		Concurrency:   cfg.Archive.Concurrency,
		MaxFileBytes:  cfg.Archive.MaxFileMB * 1024 * 1024,
		Homepages:     cfg.Archive.Homepages,
	}
	notify := false

	for i := 0; i < len(args); i++ {
		arg := args[i]
		next := func() (string, bool) {
			if i+1 >= len(args) {
				return "", false
			}
			i++
			return args[i], true
		}

		switch {
		case arg == "--out" || arg == "-out" || arg == "--dir":
			v, ok := next()
			if !ok {
				exitErr(errors.New("--out requires a directory"))
			}
			opts.Dir = v
		case arg == "--course" || arg == "-course":
			v, ok := next()
			if !ok {
				exitErr(errors.New("--course requires a course id"))
			}
			id, err := strconv.Atoi(strings.TrimSpace(v))
			if err != nil {
				exitErr(fmt.Errorf("invalid course id %q", v))
			}
			opts.Courses = append(opts.Courses, id)
		case arg == "--max-mb":
			v, ok := next()
			if !ok {
				exitErr(errors.New("--max-mb requires a number"))
			}
			mb, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
			if err != nil {
				exitErr(fmt.Errorf("invalid --max-mb %q", v))
			}
			opts.MaxFileBytes = mb * 1024 * 1024
		case arg == "--dry-run" || arg == "-n":
			opts.DryRun = true
		case arg == "--include-videos":
			opts.IncludeVideos = true
		case arg == "--no-syllabus":
			opts.SkipSyllabus = true
		case arg == "--no-homepage":
			opts.SkipHomepage = true
		case arg == "--no-submissions":
			opts.SkipSubmissions = true
		case arg == "--notify":
			notify = true
		default:
			exitErr(fmt.Errorf("unknown sync flag: %s", arg))
		}
	}

	result, err := archive.New(client, logger).Run(ctx, opts)
	if err != nil {
		if errors.Is(err, archive.ErrAuthExpired) {
			// The whole point of the scheduled job is that the user stops
			// checking by hand, so a dead cookie has to reach them.
			alertPriority(ctx, cfg, logger, 5,
				"🔒 LearningX 아카이브 중단\nCanvas 인증이 만료됐습니다. 세션 쿠키/토큰을 다시 발급해 주세요.\n\n"+err.Error())
		}
		exitErr(err)
	}

	fmt.Print(result.Summary())

	if notify {
		summary := ""
		if timeout, err := time.ParseDuration(cfg.Summary.Timeout); err == nil {
			summary = summarizeRun(ctx, summarizer.NewCLI(cfg.Summary.Command, timeout), result, logger)
		}
		if msg := syncNotification(result, opts.DryRun, summary); msg != "" {
			alert(ctx, cfg, logger, msg)
		}
	}

	if result.Failed > 0 {
		os.Exit(1)
	}
}

// syncNotification returns the Telegram message for a run, or "" when nothing
// happened worth interrupting the user for.
func syncNotification(r *archive.Result, dryRun bool, summary string) string {
	if r.Downloaded == 0 && r.Failed == 0 {
		return ""
	}

	var b strings.Builder
	verb := "저장됨"
	if dryRun {
		verb = "저장 예정 (dry-run)"
	}
	fmt.Fprintf(&b, "📥 LearningX 자료 %d개 %s\n", r.Downloaded, verb)

	// When the model summarized the run, its prose replaces the raw list: both
	// would just be the same information twice on a phone screen.
	if summary != "" {
		b.WriteString("\n" + summary + "\n")
	} else {
		const maxList = 15
		for i, f := range r.New {
			if i == maxList {
				fmt.Fprintf(&b, "…외 %d개\n", len(r.New)-maxList)
				break
			}
			tag := ""
			if f.Updated {
				tag = " (갱신)"
			}
			fmt.Fprintf(&b, "• %s / %s%s\n", f.CourseName, f.Display, tag)
		}
	}

	if r.Failed > 0 {
		fmt.Fprintf(&b, "\n⚠️ 실패 %d개", r.Failed)
	}
	return b.String()
}

// alert sends through the configured notifier, falling back to stderr so a
// failure is never swallowed just because the notifier is unreachable.
func alert(ctx context.Context, cfg config, logger *slog.Logger, msg string) {
	alertPriority(ctx, cfg, logger, 3, msg)
}

// prioritySender is implemented by notifiers that can rank a message. An
// expired credential has to stand out from a routine "3 new files" notice, or
// the alert that matters gets skimmed past with the ones that don't.
type prioritySender interface {
	Publish(ctx context.Context, title, text string, priority int) error
}

func alertPriority(ctx context.Context, cfg config, logger *slog.Logger, priority int, msg string) {
	n := buildNotifier(ctx, cfg, logger)

	var err error
	if ps, ok := n.(prioritySender); ok {
		title, body := firstLine(msg)
		err = ps.Publish(ctx, title, body, priority)
	} else {
		err = n.Send(ctx, msg)
	}

	if err != nil {
		logger.Warn("notify failed", "err", err)
		fmt.Fprintln(os.Stderr, msg)
	}
}

func firstLine(msg string) (string, string) {
	msg = strings.TrimSpace(msg)
	title, rest, found := strings.Cut(msg, "\n")
	title = strings.TrimSpace(strings.Trim(strings.TrimSpace(title), "*_"))
	if !found {
		return title, ""
	}
	return title, strings.TrimSpace(rest)
}

// handleNotifyTest sends a real message through the configured notifier. The
// alert path is only exercised when something breaks, which is the worst time
// to discover the credentials or routing were wrong — this makes it checkable
// on demand, and after every cookie rotation or redeploy.
func handleNotifyTest(ctx context.Context, cfg config, logger *slog.Logger) {
	n := buildNotifier(ctx, cfg, logger)

	title := "✅ LearningX 아카이브 알림 테스트"
	body := fmt.Sprintf("provider=%s\narchive=%s\n이 메시지가 보이면 알림 경로가 살아 있습니다.",
		cfg.Notifier.Provider, cfg.Archive.Dir)

	var err error
	if ps, ok := n.(prioritySender); ok {
		err = ps.Publish(ctx, title, body, 2)
	} else {
		err = n.Send(ctx, title+"\n"+body)
	}
	if err != nil {
		exitErr(fmt.Errorf("notify test failed: %w", err))
	}

	fmt.Printf("sent via %s\n", cfg.Notifier.Provider)
}
