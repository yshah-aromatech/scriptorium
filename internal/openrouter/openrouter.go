// Package openrouter is the thin chat-completions client behind the app's
// natural-language -> cron conversion (the HTTP half of Convert-StoToCron,
// src/Cron.psm1:246-282). It knows nothing about cron: it posts one prompt
// and hands back the model's raw reply, or a transport error whose Error()
// is the bare message. Interpreting either belongs to cron.ToCron, which
// owns every user-visible string.
package openrouter

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// systemPrompt is byte-identical with Cron.psm1:262 — the model's behavior
// is part of the parity surface.
const systemPrompt = "Convert the user's scheduling request into a single standard 5-field cron expression. Reply with ONLY the cron expression, nothing else."

// timeout matches PS's -TimeoutSec 30.
const timeout = 30 * time.Second

// Client talks to one OpenRouter-compatible endpoint.
type Client struct {
	apiKey  string
	model   string
	baseURL string
	http    *http.Client
}

// New builds a client for the real endpoint.
func New(apiKey, model string) *Client {
	return &Client{
		apiKey:  apiKey,
		model:   model,
		baseURL: "https://openrouter.ai",
		http:    &http.Client{Timeout: timeout},
	}
}

// WithBaseURL points the client at another origin (an httptest server).
func (c *Client) WithBaseURL(u string) *Client {
	c.baseURL = u
	return c
}

type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type request struct {
	Model    string    `json:"model"`
	Messages []message `json:"messages"`
}

type response struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
}

// Convert posts text and returns choices[0].message.content verbatim (empty
// when the reply carries no choice). Every failure — dial, status, decode —
// comes back as a plain error message for ToCron to wrap.
func (c *Client) Convert(text string) (string, error) {
	body, err := json.Marshal(request{
		Model: c.model,
		Messages: []message{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: text},
		},
	})
	if err != nil {
		return "", err
	}

	req, err := http.NewRequest(http.MethodPost, c.baseURL+"/api/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		// shaped like Invoke-RestMethod's own failure message, which is what
		// the PS app surfaces to the user today
		return "", fmt.Errorf("response status code does not indicate success: %d (%s)", resp.StatusCode, http.StatusText(resp.StatusCode))
	}

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	var out response
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", err
	}
	if len(out.Choices) == 0 {
		return "", nil
	}
	return out.Choices[0].Message.Content, nil
}
