package cron

// Natural-language -> cron conversion (port of Convert-StoToCron,
// src/Cron.psm1:246-282). The literal path never touches the network; the AI
// path owns all three user-visible error strings so their bytes live in one
// place, and the injected ai function stays a plain (raw, error) pair.

import "strings"

// Conversion mirrors Convert-StoToCron's @{ Expression; Source; Error }.
// Source is "literal" when the input already was a cron expression, "ai"
// otherwise (including every failure). Err empty means success.
type Conversion struct {
	Expression string
	Source     string
	Err        string
}

// ToCron converts text to a cron expression. ai receives the TRIMMED text and
// returns the model's raw reply, or a transport error whose Error() is the
// bare message. A nil ai means "no OPENROUTER_API_KEY configured" — the same
// condition PS checks before building the request.
func ToCron(text string, ai func(text string) (string, error)) Conversion {
	t := strings.TrimSpace(text)
	if Validate(t) {
		return Conversion{Expression: t, Source: "literal"}
	}
	if ai == nil {
		return Conversion{Source: "ai", Err: "not a cron expression, and OPENROUTER_API_KEY is not set for natural-language conversion"}
	}
	raw, err := ai(t)
	if err != nil {
		return Conversion{Source: "ai", Err: "OpenRouter request failed: " + err.Error()}
	}
	// models sometimes fence the answer or append prose — strip backticks and
	// take the first line that validates as a cron expression
	raw = strings.ReplaceAll(raw, "`", "")
	for _, line := range strings.Split(raw, "\n") {
		if expr := strings.TrimSpace(line); expr != "" && Validate(expr) {
			return Conversion{Expression: expr, Source: "ai"}
		}
	}
	return Conversion{Source: "ai", Err: "model returned something that isn't a cron expression: " + strings.TrimSpace(raw)}
}
