# Golden frames

Each pinned UI state is committed three ways:

| file | what it pins |
|---|---|
| `<case>-<W>x<H>.txt` | the frame with escapes stripped — the **layout** contract, and the one a human reads. Pinned at all three sizes (80×24 floor, 120×40, 200×60). |
| `<case>-<W>x<H>.ansi` | the exact frame with `COLORTERM=truecolor` — the **style** contract. Pinned at all three sizes. |
| `<case>-120x40.ansi256.ansi` | the exact frame with COLORTERM **unset** — the 256-colour downsampling contract. |

Downsampling is width-independent (lipgloss converts each token once, at theme
build time), so pinning the 256-colour frame at one size is the whole contract
rather than three copies of it.

To look at one the way a user would:

    cat internal/tui/testdata/golden/fleet-120x40.ansi

To regenerate after a deliberate design change — then read the diff before
committing it:

    go test ./internal/tui -update-golden

Every frame is rendered at a frozen clock (2026-09-02T14:30:00Z), a pinned
hostname and version, and `time.Local = UTC`, so nothing here depends on the
machine that produced it.
