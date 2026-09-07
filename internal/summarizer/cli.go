package summarizer

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// CLI runs an external single-turn LLM command (e.g. `grok -p`) and returns its
// stdout. The prompt is passed as the final argument, which is how grok's
// -p/--single flag takes it — it does not read stdin.
type CLI struct {
	argv    []string
	timeout time.Duration
}

// NewCLI builds a summarizer from a command string such as
// "/home/guzus/.local/bin/grok -p". It returns nil when the command is empty or
// its binary is not installed, so callers can treat summarization as optional
// without special-casing configuration.
func NewCLI(command string, timeout time.Duration) *CLI {
	fields := strings.Fields(strings.TrimSpace(command))
	if len(fields) == 0 {
		return nil
	}

	bin, err := resolve(fields[0])
	if err != nil {
		return nil
	}
	fields[0] = bin

	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	return &CLI{argv: fields, timeout: timeout}
}

func resolve(bin string) (string, error) {
	if strings.HasPrefix(bin, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		bin = filepath.Join(home, strings.TrimPrefix(bin, "~/"))
	}
	if strings.ContainsRune(bin, os.PathSeparator) {
		if _, err := os.Stat(bin); err != nil {
			return "", err
		}
		return bin, nil
	}
	return exec.LookPath(bin)
}

// Run executes the command with prompt as the final argument.
func (c *CLI) Run(ctx context.Context, prompt string) (string, error) {
	if c == nil {
		return "", fmt.Errorf("no summarizer configured")
	}

	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	args := append(append([]string{}, c.argv[1:]...), prompt)
	cmd := exec.CommandContext(ctx, c.argv[0], args...)

	// A model CLI left to inherit this process's stdin can block forever
	// waiting for input that never comes.
	cmd.Stdin = nil

	// Kill the whole process group, not just the command. A model CLI that
	// spawns a child leaves that child holding the stdout pipe, and Wait blocks
	// on the pipe rather than the process — so cancelling the context alone
	// does not end the call, and the timeout silently does nothing.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
			return cmd.Process.Kill()
		}
		return nil
	}
	// Backstop: if something still holds the pipe, stop waiting for it.
	cmd.WaitDelay = 5 * time.Second

	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb

	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errb.String())
		if len(msg) > 200 {
			msg = msg[:200]
		}
		return "", fmt.Errorf("%s: %w (%s)", filepath.Base(c.argv[0]), err, msg)
	}

	return strings.TrimSpace(out.String()), nil
}

// SummarizeText satisfies Summarizer.
func (c *CLI) SummarizeText(ctx context.Context, title, text string) (string, error) {
	return c.Run(ctx, fmt.Sprintf("다음 글을 3줄로 요약해 주세요. 제목: %s\n\n%s", title, text))
}

// SummarizeFile satisfies Summarizer.
func (c *CLI) SummarizeFile(ctx context.Context, filename string, data []byte) (string, error) {
	const max = 12000
	body := string(data)
	if len(body) > max {
		body = body[:max]
	}
	return c.Run(ctx, fmt.Sprintf("다음 파일 내용을 3줄로 요약해 주세요. 파일명: %s\n\n%s", filename, body))
}
