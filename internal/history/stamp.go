package history

import (
	"encoding/json"
	"time"
)

// stampLayout is the exact wire format Complete-StoRun writes
// ('yyyy-MM-ddTHH:mm:ss.fffZ', src/Runner.psm1) — always UTC, always three
// fractional digits.
const stampLayout = "2006-01-02T15:04:05.000Z"

// Stamp is a history timestamp. It marshals in the PowerShell app's exact
// format and unmarshals every format the app's own eras produced: '.fffZ',
// bare 'Z', and RFC3339 offsets (older rows were written with local Kind).
type Stamp time.Time

// IsZero reports an unset stamp — `json:",omitzero"` consults it, which is how
// an absent finishedAt stays absent on the way back out.
func (s Stamp) IsZero() bool { return time.Time(s).IsZero() }

func (s Stamp) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Time(s).UTC().Format(stampLayout))
}

func (s *Stamp) UnmarshalJSON(b []byte) error {
	if string(b) == "null" {
		*s = Stamp{}
		return nil
	}
	var raw string
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	if raw == "" {
		*s = Stamp{}
		return nil
	}
	// RFC3339 covers all three eras: fractional seconds are optional and the
	// zone is either 'Z' or a numeric offset.
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return err
	}
	*s = Stamp(t)
	return nil
}

// Time returns the stamp as a time.Time.
func (s Stamp) Time() time.Time { return time.Time(s) }
