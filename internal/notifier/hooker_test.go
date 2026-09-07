package notifier

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func hookerServer(t *testing.T, respond func(w http.ResponseWriter, body map[string]any)) (*httptest.Server, *[]map[string]any) {
	t.Helper()
	var seen []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		body["__path"] = r.URL.Path
		body["__auth"] = r.Header.Get("Authorization")
		seen = append(seen, body)
		respond(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv, &seen
}

func okResponse(chat, thread string) func(http.ResponseWriter, map[string]any) {
	return func(w http.ResponseWriter, _ map[string]any) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"ok":true,"delivered":1,"failed":0,"destinations":[{"provider":"telegram","chat_id":%q,"message_thread_id":%q,"status":"sent"}]}`, chat, thread)
	}
}

func TestHookerPublishesToConfiguredThread(t *testing.T) {
	srv, seen := hookerServer(t, okResponse("-1003805075491", "200238"))

	h := NewHooker(srv.URL, "secret", "learningx", "-1003805075491", "200238")
	if err := h.Send(context.Background(), "📥 3개 저장됨\n• a.pdf\n• b.pdf"); err != nil {
		t.Fatalf("send: %v", err)
	}

	if len(*seen) != 1 {
		t.Fatalf("expected one request, got %d", len(*seen))
	}
	got := (*seen)[0]
	if got["__path"] != "/publish/learningx" {
		t.Errorf("path = %v", got["__path"])
	}
	if got["__auth"] != "Bearer secret" {
		t.Errorf("auth = %v", got["__auth"])
	}
	if got["chat_id"] != "-1003805075491" || got["message_thread_id"] != "200238" {
		t.Errorf("routing not sent: chat=%v thread=%v", got["chat_id"], got["message_thread_id"])
	}
	// The first line becomes the title so Telegram renders a heading.
	if got["title"] != "📥 3개 저장됨" {
		t.Errorf("title = %q", got["title"])
	}
	if !strings.Contains(got["text"].(string), "a.pdf") {
		t.Errorf("text lost the body: %q", got["text"])
	}
}

// hooker's `delivered` count says only that some route accepted the message,
// never which chat got it — so a misroute must be caught by comparing the
// echoed destination, or a silently redirected alert looks like success.
func TestHookerRejectsMisroutedDelivery(t *testing.T) {
	srv, _ := hookerServer(t, okResponse("-100999999", "1"))

	h := NewHooker(srv.URL, "secret", "learningx", "-1003805075491", "200238")
	err := h.Send(context.Background(), "hello")
	if err == nil {
		t.Fatal("expected an error when hooker delivered to a different chat")
	}
	if !strings.Contains(err.Error(), "routed elsewhere") {
		t.Fatalf("error = %v", err)
	}
}

func TestHookerReportsFailedDelivery(t *testing.T) {
	srv, _ := hookerServer(t, func(w http.ResponseWriter, _ map[string]any) {
		fmt.Fprint(w, `{"ok":true,"delivered":0,"failed":1,"destinations":[]}`)
	})

	h := NewHooker(srv.URL, "k", "learningx", "-100", "1")
	if err := h.Send(context.Background(), "hi"); err == nil {
		t.Fatal("expected an error when nothing was delivered")
	}
}

func TestHookerSurfacesHTTPError(t *testing.T) {
	srv, _ := hookerServer(t, func(w http.ResponseWriter, _ map[string]any) {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"error":"bad token"}`)
	})

	h := NewHooker(srv.URL, "wrong", "learningx", "-100", "1")
	err := h.Send(context.Background(), "hi")
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("error = %v, want the 401 surfaced", err)
	}
}

func TestHookerPriorityIsSent(t *testing.T) {
	srv, seen := hookerServer(t, okResponse("-100", "1"))

	h := NewHooker(srv.URL, "k", "", "-100", "1")
	if err := h.Publish(context.Background(), "🔒 인증 만료", "재발급 필요", 5); err != nil {
		t.Fatalf("publish: %v", err)
	}
	got := (*seen)[0]
	if got["priority"].(float64) != 5 {
		t.Errorf("priority = %v, want 5", got["priority"])
	}
	// An unset topic must still resolve to a real path.
	if got["__path"] != "/publish/learningx" {
		t.Errorf("default topic path = %v", got["__path"])
	}
}
