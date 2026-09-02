package openrouter_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yshah-aromatech/scriptorium/internal/openrouter"
)

type capture struct {
	path string
	auth string
	ct   string
	body map[string]any
}

// stub serves one canned completion and records the request.
func stub(t *testing.T, status int, content string) (*openrouter.Client, *capture) {
	t.Helper()
	cap := &capture{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cap.path = r.URL.Path
		cap.auth = r.Header.Get("Authorization")
		cap.ct = r.Header.Get("Content-Type")
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &cap.body)
		if status != 200 {
			w.WriteHeader(status)
			_, _ = io.WriteString(w, "boom")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		resp := map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": content}}}}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(srv.Close)
	return openrouter.New("test-key", "some/model").WithBaseURL(srv.URL), cap
}

func TestConvertReturnsTheReplyContent(t *testing.T) {
	c, _ := stub(t, 200, "0 17 * * *")
	got, err := c.Convert("every day at 5pm")
	if err != nil {
		t.Fatal(err)
	}
	if got != "0 17 * * *" {
		t.Errorf("Convert = %q, want %q", got, "0 17 * * *")
	}
}

func TestConvertRequestShape(t *testing.T) {
	c, cap := stub(t, 200, "0 17 * * *")
	if _, err := c.Convert("every day at 5pm"); err != nil {
		t.Fatal(err)
	}
	if cap.path != "/api/v1/chat/completions" {
		t.Errorf("path = %q, want /api/v1/chat/completions", cap.path)
	}
	if cap.auth != "Bearer test-key" {
		t.Errorf("Authorization = %q, want %q", cap.auth, "Bearer test-key")
	}
	if !strings.HasPrefix(cap.ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", cap.ct)
	}
	if cap.body["model"] != "some/model" {
		t.Errorf("model = %v, want some/model", cap.body["model"])
	}
	msgs, ok := cap.body["messages"].([]any)
	if !ok || len(msgs) != 2 {
		t.Fatalf("messages = %v, want 2 entries", cap.body["messages"])
	}
	sys := msgs[0].(map[string]any)
	usr := msgs[1].(map[string]any)
	if sys["role"] != "system" || usr["role"] != "user" {
		t.Fatalf("roles = %v / %v, want system / user", sys["role"], usr["role"])
	}
	// byte-exact with Cron.psm1:262
	const wantSys = "Convert the user's scheduling request into a single standard 5-field cron expression. Reply with ONLY the cron expression, nothing else."
	if sys["content"] != wantSys {
		t.Errorf("system prompt = %q\nwant %q", sys["content"], wantSys)
	}
	if usr["content"] != "every day at 5pm" {
		t.Errorf("user content = %q, want %q", usr["content"], "every day at 5pm")
	}
}

func TestConvertServerErrorIsATransportError(t *testing.T) {
	c, _ := stub(t, 500, "")
	_, err := c.Convert("whatever")
	if err == nil {
		t.Fatal("want an error on HTTP 500")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("error = %q, want it to mention the status", err.Error())
	}
}

func TestConvertRefusedConnectionIsATransportError(t *testing.T) {
	// a server that is closed before the call: the dial is refused
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close()
	_, err := openrouter.New("k", "m").WithBaseURL(url).Convert("whatever")
	if err == nil {
		t.Fatal("want an error when the endpoint refuses the connection")
	}
}

func TestConvertEmptyChoicesReturnsEmptyString(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"choices":[]}`)
	}))
	defer srv.Close()
	got, err := openrouter.New("k", "m").WithBaseURL(srv.URL).Convert("x")
	if err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Errorf("Convert = %q, want empty", got)
	}
}
