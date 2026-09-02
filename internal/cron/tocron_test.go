package cron_test

import (
	"errors"
	"testing"

	"github.com/yshah-aromatech/scriptorium/internal/cron"
)

// panicAI proves the literal path never reaches the network.
func panicAI(string) (string, error) { panic("ai must not be called for a literal cron expression") }

func TestToCronLiteralShortCircuits(t *testing.T) {
	for _, in := range []string{"*/5 * * * *", "  0 2 * * *  ", "@daily"} {
		got := cron.ToCron(in, panicAI)
		if got.Source != "literal" {
			t.Errorf("ToCron(%q).Source = %q, want \"literal\"", in, got.Source)
		}
		if got.Err != "" {
			t.Errorf("ToCron(%q).Err = %q, want empty", in, got.Err)
		}
		if got.Expression != trimmed(in) {
			t.Errorf("ToCron(%q).Expression = %q, want %q", in, got.Expression, trimmed(in))
		}
	}
}

func trimmed(s string) string {
	for len(s) > 0 && (s[0] == ' ' || s[0] == '\t') {
		s = s[1:]
	}
	for len(s) > 0 && (s[len(s)-1] == ' ' || s[len(s)-1] == '\t') {
		s = s[:len(s)-1]
	}
	return s
}

func TestToCronNilAIErrorIsByteExact(t *testing.T) {
	got := cron.ToCron("every tuesday at noon", nil)
	const want = "not a cron expression, and OPENROUTER_API_KEY is not set for natural-language conversion"
	if got.Err != want {
		t.Errorf("Err = %q\nwant %q", got.Err, want)
	}
	if got.Source != "ai" {
		t.Errorf("Source = %q, want \"ai\"", got.Source)
	}
	if got.Expression != "" {
		t.Errorf("Expression = %q, want empty", got.Expression)
	}
}

func TestToCronParsesAIReplies(t *testing.T) {
	cases := []struct {
		name  string
		reply string
		want  string
	}{
		{"fenced", "```\n0 17 * * *\n```", "0 17 * * *"},
		{"fenced inline", "`0 17 * * *`", "0 17 * * *"},
		{"prose then expression", "Sure! Here you go:\n0 17 * * *\nHope that helps.", "0 17 * * *"},
		{"bare", "*/15 * * * *", "*/15 * * * *"},
		{"crlf", "here:\r\n@daily\r\n", "@daily"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := cron.ToCron("every day at 5pm", func(string) (string, error) { return c.reply, nil })
			if got.Expression != c.want || got.Source != "ai" || got.Err != "" {
				t.Errorf("ToCron = %+v, want Expression %q Source \"ai\" no Err", got, c.want)
			}
		})
	}
}

func TestToCronPassesTheTrimmedTextToTheModel(t *testing.T) {
	var seen string
	cron.ToCron("  every tuesday  ", func(s string) (string, error) { seen = s; return "0 0 * * 2", nil })
	if seen != "every tuesday" {
		t.Errorf("ai received %q, want %q", seen, "every tuesday")
	}
}

func TestToCronNonExpressionReplyErrorIsByteExact(t *testing.T) {
	got := cron.ToCron("gibberish", func(string) (string, error) {
		return "  I'm sorry, I can't help with that.\n", nil
	})
	const want = "model returned something that isn't a cron expression: I'm sorry, I can't help with that."
	if got.Err != want {
		t.Errorf("Err = %q\nwant %q", got.Err, want)
	}
	if got.Source != "ai" {
		t.Errorf("Source = %q, want \"ai\"", got.Source)
	}
}

func TestToCronTransportErrorIsByteExact(t *testing.T) {
	got := cron.ToCron("gibberish", func(string) (string, error) {
		return "", errors.New("connection refused")
	})
	const want = "OpenRouter request failed: connection refused"
	if got.Err != want {
		t.Errorf("Err = %q\nwant %q", got.Err, want)
	}
	if got.Source != "ai" {
		t.Errorf("Source = %q, want \"ai\"", got.Source)
	}
}
