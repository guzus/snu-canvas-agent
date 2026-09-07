package notifier

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// HookerNotifier publishes through hooker, which fans a message out to
// Telegram. Routing is explicit: hooker sends directly to chat_id when it is
// set, skipping topic-route lookup.
type HookerNotifier struct {
	baseURL  string
	apiKey   string
	topic    string
	chatID   string
	threadID string
	client   *http.Client
}

func NewHooker(baseURL, apiKey, topic, chatID, threadID string) *HookerNotifier {
	if topic == "" {
		topic = "learningx"
	}
	return &HookerNotifier{
		baseURL:  strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		apiKey:   strings.TrimSpace(apiKey),
		topic:    strings.TrimSpace(topic),
		chatID:   strings.TrimSpace(chatID),
		threadID: strings.TrimSpace(threadID),
		client:   &http.Client{Timeout: 20 * time.Second},
	}
}

// Send publishes at normal priority. The first line becomes the title so the
// Telegram message renders with a heading instead of one undifferentiated blob.
func (h *HookerNotifier) Send(ctx context.Context, message string) error {
	title, body := splitTitle(message)
	return h.Publish(ctx, title, body, 3)
}

// Publish sends with an explicit priority. A dead credential should not arrive
// looking like a routine "3 new files" notice.
func (h *HookerNotifier) Publish(ctx context.Context, title, text string, priority int) error {
	if h.baseURL == "" {
		return fmt.Errorf("hooker url not configured")
	}
	if text == "" {
		text = title
	}

	payload := map[string]any{
		"title":    title,
		"text":     text,
		"priority": priority,
		"tags":     []string{"learningx", "archive"},
	}
	if h.chatID != "" {
		payload["chat_id"] = h.chatID
	}
	if h.threadID != "" {
		payload["message_thread_id"] = h.threadID
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	url := fmt.Sprintf("%s/publish/%s", h.baseURL, h.topic)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if h.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+h.apiKey)
	}

	resp, err := h.client.Do(req)
	if err != nil {
		return fmt.Errorf("hooker publish: %w", err)
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
	if resp.StatusCode >= 400 {
		return fmt.Errorf("hooker error %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}

	return h.verifyDestination(raw)
}

// verifyDestination checks the message actually went where it was addressed.
// hooker's `delivered` count only says some route accepted the message — it
// does not say which chat or forum thread got it, so a misrouted alert would
// otherwise be indistinguishable from a correct one.
func (h *HookerNotifier) verifyDestination(raw []byte) error {
	var out struct {
		OK           bool `json:"ok"`
		Delivered    int  `json:"delivered"`
		Failed       int  `json:"failed"`
		Destinations []struct {
			ChatID          string `json:"chat_id"`
			MessageThreadID string `json:"message_thread_id"`
			Status          string `json:"status"`
		} `json:"destinations"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil // delivered with a body we don't recognise; don't fail the run
	}

	if out.Delivered == 0 && out.Failed > 0 {
		return fmt.Errorf("hooker delivered nothing (%d failed)", out.Failed)
	}
	if h.chatID == "" || len(out.Destinations) == 0 {
		return nil
	}

	for _, d := range out.Destinations {
		if d.ChatID != h.chatID {
			continue
		}
		if h.threadID != "" && d.MessageThreadID != "" && d.MessageThreadID != h.threadID {
			continue
		}
		if d.Status == "failed" {
			return fmt.Errorf("hooker delivery to chat %s failed", d.ChatID)
		}
		return nil
	}
	return fmt.Errorf("hooker routed elsewhere: wanted chat %s thread %s, got %v",
		h.chatID, h.threadID, out.Destinations)
}

// splitTitle takes the first line as a title, stripping markdown emphasis so
// it reads cleanly as a heading.
func splitTitle(message string) (string, string) {
	message = strings.TrimSpace(message)
	title, rest, found := strings.Cut(message, "\n")
	title = strings.TrimSpace(strings.Trim(strings.TrimSpace(title), "*_"))
	if !found {
		return title, ""
	}
	return title, strings.TrimSpace(rest)
}
