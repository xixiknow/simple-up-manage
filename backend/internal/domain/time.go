package domain

import (
	"strings"
	"time"
)

// LooseTime scans SQLite string timestamps and Postgres time.Time values.
type LooseTime time.Time

func (t *LooseTime) Scan(value any) error {
	parsed, ok := ParseLooseTime(value)
	if !ok {
		*t = LooseTime{}
		return nil
	}
	*t = LooseTime(parsed)
	return nil
}

func (t LooseTime) Time() time.Time {
	return time.Time(t)
}

func ParseLooseTime(value any) (time.Time, bool) {
	switch v := value.(type) {
	case nil:
		return time.Time{}, false
	case time.Time:
		return v, !v.IsZero()
	case *time.Time:
		if v == nil || v.IsZero() {
			return time.Time{}, false
		}
		return *v, true
	case []byte:
		return parseTimeString(string(v))
	case string:
		return parseTimeString(v)
	default:
		return time.Time{}, false
	}
}

func parseTimeString(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false
	}
	layouts := []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02 15:04:05.999999999-07:00",
		"2006-01-02 15:04:05.999999999+07:00",
		"2006-01-02 15:04:05.999999999",
		"2006-01-02 15:04:05",
		"2006-01-02T15:04:05.999999999Z07:00",
	}
	for _, layout := range layouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t, true
		}
		if t, err := time.ParseInLocation(layout, s, time.Local); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}
