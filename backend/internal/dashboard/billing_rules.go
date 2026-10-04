package dashboard

import (
	"strconv"
	"strings"
	"sync"
	"time"

	"simple-up-manage/internal/config"
)

// TimeRule surcharges input/output/cache-read prices during configured time
// windows (provider peak hours). Windows are half-open [start, end) in TZ and
// wrap midnight when end <= start. The first rule whose ModelMatch occurs in
// the (lowercased) model name wins; with no rules configured the built-in
// DeepSeek peak-valley rule applies.
type TimeRule struct {
	Match       string
	TZ          *time.Location
	WeekendTZ   *time.Location
	SkipWeekend bool
	Windows     []TimeWindow
}

type TimeWindow struct {
	Days       []time.Weekday // empty = every day, judged in the rule's TZ
	StartMin   int
	EndMin     int
	Multiplier float64
}

// EffortRule multiplies the whole cost when a request's reasoning effort level
// matches a configured key (e.g. max: 3.0).
type EffortRule struct {
	Match       string
	Multipliers map[string]float64
}

var billingRulesMu sync.RWMutex
var billingTimeRules []TimeRule
var billingEffortRules []EffortRule

// ConfigureBillingRules installs the rule tables from config; nil/empty time
// rules fall back to the built-in DeepSeek peak-valley rule. Invalid rules are
// dropped, never fatal: a broken rule table must not take down billing.
func ConfigureBillingRules(cfg config.Billing) {
	billingRulesMu.Lock()
	defer billingRulesMu.Unlock()
	billingTimeRules = billingTimeRules[:0]
	for _, r := range cfg.TimeRules {
		if tr, ok := parseTimeRule(r); ok {
			billingTimeRules = append(billingTimeRules, tr)
		}
	}
	billingEffortRules = billingEffortRules[:0]
	for _, r := range cfg.EffortRules {
		if r.ModelMatch == "" || len(r.Multipliers) == 0 {
			continue
		}
		mult := make(map[string]float64, len(r.Multipliers))
		for effort, m := range r.Multipliers {
			level := strings.ToLower(strings.TrimSpace(effort))
			if level == "" || m <= 0 {
				continue
			}
			mult[level] = m
		}
		if len(mult) > 0 {
			billingEffortRules = append(billingEffortRules, EffortRule{Match: strings.ToLower(r.ModelMatch), Multipliers: mult})
		}
	}
}

func parseTimeRule(r config.TimeRule) (TimeRule, bool) {
	match := strings.ToLower(strings.TrimSpace(r.ModelMatch))
	if match == "" || len(r.Windows) == 0 {
		return TimeRule{}, false
	}
	tz := loadRuleLocation(r.TZ, time.UTC)
	out := TimeRule{
		Match:       match,
		TZ:          tz,
		SkipWeekend: r.SkipWeekend,
		WeekendTZ:   loadRuleLocation(r.WeekendTZ, tz),
	}
	for _, w := range r.Windows {
		start, okStart := parseRuleClock(w.Start)
		end, okEnd := parseRuleClock(w.End)
		if !okStart || !okEnd || w.Multiplier <= 0 {
			continue
		}
		tw := TimeWindow{StartMin: start, EndMin: end, Multiplier: w.Multiplier}
		for _, d := range w.Days {
			if day, ok := parseRuleWeekday(d); ok {
				tw.Days = append(tw.Days, day)
			}
		}
		out.Windows = append(out.Windows, tw)
	}
	if len(out.Windows) == 0 {
		return TimeRule{}, false
	}
	return out, true
}

func loadRuleLocation(name string, fallback *time.Location) *time.Location {
	name = strings.TrimSpace(name)
	if name == "" {
		return fallback
	}
	if strings.EqualFold(name, "UTC") {
		return time.UTC
	}
	if strings.EqualFold(name, "Local") {
		return time.Local
	}
	if offset, ok := fixedOffset(name); ok {
		return time.FixedZone(name, offset)
	}
	if loc, err := time.LoadLocation(name); err == nil {
		return loc
	}
	return fallback
}

// fixedOffset accepts "+08:00" / "-0530"-style fixed zones.
func fixedOffset(name string) (int, bool) {
	if len(name) != 6 || (name[0] != '+' && name[0] != '-') || name[3] != ':' {
		return 0, false
	}
	sign := 1
	if name[0] == '-' {
		sign = -1
	}
	h, ok1 := twoDigits(name[1:3])
	m, ok2 := twoDigits(name[4:6])
	if !ok1 || !ok2 {
		return 0, false
	}
	return sign * (h*3600 + m*60), true
}

func twoDigits(s string) (int, bool) {
	if len(s) != 2 || s[0] < '0' || s[0] > '9' || s[1] < '0' || s[1] > '9' {
		return 0, false
	}
	return int(s[0]-'0')*10 + int(s[1]-'0'), true
}

func parseRuleClock(s string) (int, bool) {
	s = strings.TrimSpace(s)
	parts := strings.Split(s, ":")
	if len(parts) != 2 {
		return 0, false
	}
	h, err := strconv.Atoi(parts[0])
	if err != nil || h < 0 || h > 23 {
		return 0, false
	}
	m, err := strconv.Atoi(parts[1])
	if err != nil || m < 0 || m > 59 {
		return 0, false
	}
	return h*60 + m, true
}

var ruleWeekdays = map[string]time.Weekday{
	"sunday": time.Sunday, "sun": time.Sunday,
	"monday": time.Monday, "mon": time.Monday,
	"tuesday": time.Tuesday, "tue": time.Tuesday, "tues": time.Tuesday,
	"wednesday": time.Wednesday, "wed": time.Wednesday,
	"thursday": time.Thursday, "thu": time.Thursday, "thur": time.Thursday, "thurs": time.Thursday,
	"friday": time.Friday, "fri": time.Friday,
	"saturday": time.Saturday, "sat": time.Saturday,
}

func parseRuleWeekday(s string) (time.Weekday, bool) {
	d, ok := ruleWeekdays[strings.ToLower(strings.TrimSpace(s))]
	return d, ok
}

// deepseekPeakHours are DeepSeek's official peak windows (01:00–04:00 and
// 06:00–10:00 UTC, weekdays only; Beijing-time weekends stay off-peak all
// day). Peak price = 2× off-peak price.
func builtinDeepSeekTimeRules() []TimeRule {
	return []TimeRule{{
		Match:       "deepseek",
		TZ:          time.UTC,
		WeekendTZ:   time.FixedZone("Asia/Shanghai", 8*3600),
		SkipWeekend: true,
		Windows: []TimeWindow{
			{StartMin: 60, EndMin: 240, Multiplier: 2},
			{StartMin: 360, EndMin: 600, Multiplier: 2},
		},
	}}
}

func snapshotRules() ([]TimeRule, []EffortRule) {
	billingRulesMu.RLock()
	defer billingRulesMu.RUnlock()
	timeRules := billingTimeRules
	if len(timeRules) == 0 {
		timeRules = builtinDeepSeekTimeRules()
	}
	return timeRules, billingEffortRules
}

// TimeMultiplier returns the peak/off-peak surcharge for a model at a given
// moment (1 = none).
func TimeMultiplier(model string, at time.Time) float64 {
	rules, _ := snapshotRules()
	if at.IsZero() {
		at = time.Now()
	}
	name := strings.ToLower(model)
	for _, rule := range rules {
		if rule.Match != "" && !strings.Contains(name, rule.Match) {
			continue
		}
		local := at.In(rule.TZ)
		if rule.SkipWeekend {
			weekend := at.In(rule.WeekendTZ).Weekday()
			if weekend == time.Saturday || weekend == time.Sunday {
				return 1
			}
		}
		minute := local.Hour()*60 + local.Minute()
		for _, w := range rule.Windows {
			if len(w.Days) > 0 {
				matched := false
				for _, d := range w.Days {
					if d == local.Weekday() {
						matched = true
						break
					}
				}
				if !matched {
					continue
				}
			}
			if w.StartMin <= w.EndMin {
				if minute >= w.StartMin && minute < w.EndMin {
					return w.Multiplier
				}
			} else if minute >= w.StartMin || minute < w.EndMin {
				return w.Multiplier
			}
		}
		return 1
	}
	return 1
}

// EffortMultiplier returns the reasoning-effort surcharge for a model (1 =
// none). Unknown effort levels never surcharge.
func EffortMultiplier(model, effort string) float64 {
	_, rules := snapshotRules()
	if len(rules) == 0 || strings.TrimSpace(effort) == "" {
		return 1
	}
	name := strings.ToLower(model)
	level := strings.ToLower(strings.TrimSpace(effort))
	for _, rule := range rules {
		if !strings.Contains(name, rule.Match) {
			continue
		}
		if m, ok := rule.Multipliers[level]; ok && m > 0 {
			return m
		}
		return 1
	}
	return 1
}
