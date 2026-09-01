// Package webhook is the n8n delivery client: POST with one retry, and a
// dead-letter queue for reports that could not be delivered, so a cron run's
// report is not silently lost when n8n is down. Port of Send-StoWebhook /
// Send-StoWebhookRaw / Send-StoWebhookQueue / Send-StoWebhookTest
// (src/Runner.psm1).
//
// The queue protocol is kept VERBATIM from the PowerShell app — the rename
// interlock, the 10-minute stale reclaim and the 50-per-flush cap — because
// both implementations may run side by side during the migration and must
// interlock on the same files.
package webhook

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// flushCap is how many queued reports one flush may send: a week-long
// backlog must not freeze run completion.
const flushCap = 50

// staleFlush is how long a .flush file must sit untouched before it is
// treated as abandoned by a flusher that died mid-pass.
const staleFlush = 10 * time.Minute

// retryGap is the pause between the first attempt and the retry.
const retryGap = 2 * time.Second

// stampLayout is the timestamp format the PS app writes ('yyyy-MM-ddTHH:mm:ss.fffZ').
const stampLayout = "2006-01-02T15:04:05.000Z"

// Client posts payloads to one webhook URL and owns its dead-letter queue
// file. Safe for concurrent use.
type Client struct {
	url       string
	queueFile string
	hc        *http.Client
	sleep     func(time.Duration)

	// mu orders THIS process's queue-file mutations. Cross-process
	// serialization is the .flush interlock below, which also covers
	// this process — mu only closes the window where a concurrent
	// in-process append could be dropped by a flush's rewrite. It is
	// deliberately never held across a network send.
	mu sync.Mutex
}

// Option configures a Client.
type Option func(*Client)

// WithSleep replaces the retry pause (tests must not wait two seconds).
func WithSleep(f func(time.Duration)) Option {
	return func(c *Client) { c.sleep = f }
}

// NewClient returns a client for a webhook URL (empty = webhooks disabled),
// a per-attempt timeout and a dead-letter queue file
// (config.Paths.WebhookQueueFile). The caller resolves the URL: env
// N8N_WEBHOOK_URL wins over config.
func NewClient(url string, timeout time.Duration, queueFile string, opts ...Option) *Client {
	c := &Client{
		url:       url,
		queueFile: queueFile,
		hc:        &http.Client{Timeout: timeout},
		sleep:     time.Sleep,
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// Host is the machine name as the PS app reports it: [Environment]::MachineName,
// which on Unix is the hostname truncated at the first dot. It lives here
// because the payloads that carry it (run reports, test pings, missed-run
// alerts) all go out through this client.
func Host() string {
	h, err := os.Hostname()
	if err != nil {
		return ""
	}
	name, _, _ := strings.Cut(h, ".")
	return name
}

// SendRaw posts one already-marshalled body. Only a 2xx is a delivery: the
// PS app's Invoke-RestMethod throws on anything else.
func (c *Client) SendRaw(body []byte) bool {
	if c.url == "" {
		return false
	}
	req, err := http.NewRequest(http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return false
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.hc.Do(req)
	if err != nil {
		return false
	}
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode >= 200 && resp.StatusCode < 300
}

// Send delivers a payload, retrying once after a two-second pause. A success
// also drains the dead-letter queue behind it. A failure queues the payload
// unless event is "test" — test pings are interactive, the user already sees
// the failure. event is passed explicitly rather than read back out of the
// payload, so the queue exemption can never depend on how a caller happened
// to shape its struct.
func (c *Client) Send(payload any, event string) bool {
	if c.url == "" {
		return false
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return false
	}
	ok := c.SendRaw(body)
	if !ok {
		c.sleep(retryGap)
		ok = c.SendRaw(body)
	}
	if ok {
		c.FlushQueue()
		return true
	}
	if event != "test" {
		c.enqueue(body)
	}
	return false
}

// SendTest posts the interactive connectivity ping.
func (c *Client) SendTest() bool {
	payload := struct {
		Event string `json:"event"`
		Host  string `json:"host"`
		At    string `json:"at"`
	}{"test", Host(), time.Now().UTC().Format(stampLayout)}
	return c.Send(payload, "test")
}

// enqueue appends one compact line in a single O_APPEND write, so concurrent
// appenders in other processes cannot interleave mid-line.
func (c *Client) enqueue(body []byte) {
	if c.queueFile == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	f, err := os.OpenFile(c.queueFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	line := make([]byte, 0, len(body)+1)
	line = append(append(line, body...), '\n')
	_, _ = f.Write(line)
	_ = f.Close()
}

// FlushQueue resends the dead-letter queue in order, stops at the first
// failure and keeps whatever could not be sent. It returns how many were
// delivered.
//
// The queue file is claimed (moved aside) first, so two runs completing
// together cannot both flush — duplicate deliveries — or clobber each
// other's rewrite.
func (c *Client) FlushQueue() int {
	qf := c.queueFile
	if qf == "" {
		return 0
	}
	if _, err := os.Stat(qf); err != nil {
		return 0
	}
	flushFile := qf + ".flush"
	if !claim(qf, flushFile) {
		reclaimStale(qf, flushFile)
		return 0
	}

	lines := readLines(flushFile)
	sent := 0
	for _, line := range lines {
		if sent >= flushCap {
			break
		}
		if !c.SendRaw([]byte(line)) {
			break
		}
		sent++
	}

	remaining := lines[sent:]
	if len(remaining) == 0 {
		_ = os.Remove(flushFile)
		return sent
	}
	// the unsent backlog is OLDER than anything queued while we flushed, so
	// it goes back in front of it
	c.mu.Lock()
	defer c.mu.Unlock()
	rebuilt := append(append([]string(nil), remaining...), readLines(qf)...)
	if writeLines(flushFile, rebuilt) == nil {
		// os.Rename overwrites, which is what PS's Move(..., $true) does
		_ = os.Rename(flushFile, qf)
	}
	return sent
}

// claim moves the queue file aside, exclusively. os.Rename would silently
// OVERWRITE an existing .flush (unlike .NET's File.Move, which throws), and
// that would drop another flusher's whole backlog — so the claim is a link,
// which is the atomic fail-if-exists primitive POSIX does offer, followed by
// unlinking the original name. Linking keeps the inode, so the .flush mtime
// still dates from the last queue append, which is what the stale check below
// measures.
func claim(qf, flushFile string) bool {
	if err := os.Link(qf, flushFile); err != nil {
		return false
	}
	_ = os.Remove(qf)
	return true
}

// reclaimStale rescues the backlog of a flusher that died mid-pass. PS does
// `Get-Content .flush | Add-Content queue`, so reclaimed lines land AFTER
// whatever is already queued; the order is part of the on-disk contract.
func reclaimStale(qf, flushFile string) {
	st, err := os.Stat(flushFile)
	if err != nil || time.Since(st.ModTime()) <= staleFlush {
		return
	}
	lines := readLines(flushFile)
	if len(lines) > 0 {
		f, oerr := os.OpenFile(qf, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if oerr != nil {
			return
		}
		_, werr := f.WriteString(strings.Join(lines, "\n") + "\n")
		cerr := f.Close()
		if werr != nil || cerr != nil {
			return // leave .flush in place rather than lose the backlog
		}
	}
	_ = os.Remove(flushFile)
}

// readLines returns a queue file's non-blank lines; a missing or unreadable
// file is an empty queue, never an error (PS: -ErrorAction SilentlyContinue
// plus a `Where-Object { $_ }` truthiness filter).
func readLines(path string) []string {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var out []string
	for _, l := range strings.Split(string(b), "\n") {
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}

// writeLines replaces a file with one line per entry, newline-terminated.
func writeLines(path string, lines []string) error {
	return os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644)
}
