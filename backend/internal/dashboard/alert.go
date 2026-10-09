package dashboard

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"sort"
	"strings"
	"time"

	"simple-up-manage/internal/domain"

	"gorm.io/gorm/clause"
)

// NoticeKindBalanceAlert marks low-balance warnings in the unified inbox.
const NoticeKindBalanceAlert = "balance_alert"

// SilenceStateTTL drops per-provider alert timestamps older than this so the
// state column cannot grow unboundedly for removed providers.
const SilenceStateTTL = 7 * 24 * time.Hour

// ServerChanSendTimeout bounds one push call; the check job runs on a ticker
// and must not stack up behind a hanging endpoint.
const ServerChanSendTimeout = 10 * time.Second

// LoadAlertSettings reads the single-row alert config, creating it on first use.
func (s *Service) LoadAlertSettings() domain.AlertSettings {
	row := domain.DefaultAlertSettings()
	if s == nil || s.db == nil {
		return row
	}
	_ = s.db.FirstOrCreate(&row, domain.AlertSettings{ID: 1}).Error
	row.Normalize()
	return row
}

// SaveAlertSettings persists the alert config. SendKey is expected to arrive
// already encrypted; the handler does the crypto.
func (s *Service) SaveAlertSettings(in domain.AlertSettings) (domain.AlertSettings, error) {
	in.Normalize()
	in.ID = 1
	in.UpdatedAt = time.Now().UTC()
	if s == nil || s.db == nil {
		return in, nil
	}
	if err := s.db.Clauses(clause.OnConflict{UpdateAll: true}).Create(&in).Error; err != nil {
		return domain.AlertSettings{}, err
	}
	return s.LoadAlertSettings(), nil
}

type alertTarget struct {
	UrgentItem
	Hours float64
}

// filterAlertItems picks enabled, known-balance providers whose projected
// hours are below the threshold (including hours=0, i.e. depleted).
func filterAlertItems(items []UrgentItem, threshold float64) []alertTarget {
	var out []alertTarget
	for _, it := range items {
		if it.HoursLeft == nil || *it.HoursLeft >= threshold {
			continue
		}
		out = append(out, alertTarget{UrgentItem: it, Hours: *it.HoursLeft})
	}
	return out
}

// silenceDue applies the per-provider repeat window from the stored state.
func silenceDue(state map[uint]time.Time, targets []alertTarget, silence time.Duration, now time.Time) []alertTarget {
	due := make([]alertTarget, 0, len(targets))
	for _, t := range targets {
		if last, ok := state[t.ProviderID]; ok && now.Sub(last) < silence {
			continue
		}
		due = append(due, t)
	}
	return due
}

func loadSilenceState(raw string) map[uint]time.Time {
	out := map[uint]time.Time{}
	if strings.TrimSpace(raw) == "" {
		return out
	}
	var decoded map[uint]int64
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		return out
	}
	for id, unix := range decoded {
		out[id] = time.Unix(unix, 0)
	}
	return out
}

func saveSilenceState(state map[uint]time.Time) string {
	encoded := make(map[uint]int64, len(state))
	for id, at := range state {
		encoded[id] = at.Unix()
	}
	raw, err := json.Marshal(encoded)
	if err != nil {
		return ""
	}
	return string(raw)
}

// buildAlertMessage renders one aggregated markdown push for all due providers.
// Callers sort targets by Hours ascending, so targets[0] is the most urgent.
func buildAlertMessage(targets []alertTarget) (title, desp string) {
	if len(targets) == 1 {
		t := targets[0]
		if t.Hours <= 0 {
			title = fmt.Sprintf("余额不足预警：%s 余额已耗尽", t.Name)
		} else {
			title = fmt.Sprintf("余额不足预警：%s 预计仅剩 %.1f 小时", t.Name, t.Hours)
		}
	} else if targets[0].Hours <= 0 {
		title = fmt.Sprintf("余额不足预警：%d 个提供商余额已耗尽或即将耗尽", len(targets))
	} else {
		title = fmt.Sprintf("余额不足预警：%d 个提供商告急，最紧的预计仅剩 %.1f 小时", len(targets), targets[0].Hours)
	}
	var b strings.Builder
	b.WriteString("以下提供商按当前消耗速率预计即将耗尽余额：\n\n")
	for _, t := range targets {
		fmt.Fprintf(&b, "- **%s**：预计 %.1f 小时", t.Name, t.Hours)
		if t.BalanceUSD != nil {
			fmt.Fprintf(&b, "，余额 $%.2f", *t.BalanceUSD)
		}
		if t.Consumed24hUSD != nil && *t.Consumed24hUSD > 0 {
			fmt.Fprintf(&b, "，近 24h 消耗 $%.2f", *t.Consumed24hUSD)
		}
		if t.Hours <= 0 {
			b.WriteString("（已耗尽）")
		}
		b.WriteString("\n")
	}
	b.WriteString("\n> 预计小时数按余额、近 24 小时已知消耗水平与近 7 天分时段消耗分布逐小时推算，仅统计本网关已知流量；流量不足一天的提供商按平摊速率估算。\n")
	return title, b.String()
}

// SendServerChan pushes via ServerChan (³ for sctp-prefixed keys, Turbo
// otherwise), matching the official SDK's endpoint auto-detection.
func SendServerChan(ctx context.Context, sendKey, title, desp string) error {
	sendKey = strings.TrimSpace(sendKey)
	if sendKey == "" {
		return fmt.Errorf("send key is empty")
	}
	var url string
	if strings.HasPrefix(sendKey, "sctp") {
		url = fmt.Sprintf("https://%s.push.ft07.com/send", sendKey)
	} else {
		url = fmt.Sprintf("https://sctapi.ftqq.com/%s.send", sendKey)
	}
	raw, err := json.Marshal(map[string]string{"title": title, "desp": desp})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	var res struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(body, &res); err != nil {
		if resp.StatusCode >= 400 {
			return fmt.Errorf("serverchan http %d", resp.StatusCode)
		}
		return nil
	}
	if res.Code != 0 {
		msg := strings.TrimSpace(res.Message)
		if msg == "" {
			msg = fmt.Sprintf("code %d", res.Code)
		}
		return fmt.Errorf("serverchan: %s", msg)
	}
	return nil
}

// CheckBalanceAlerts runs one alert pass: projection via urgent(), threshold
// and silence filtering, one aggregated push, then state + inbox updates.
// Push failure is logged but the silence state still advances, so a flapping
// endpoint cannot turn the 5-minute ticker into a notification storm.
func (s *Service) CheckBalanceAlerts(ctx context.Context) {
	if s == nil || s.db == nil {
		return
	}
	cfg := s.LoadAlertSettings()
	if !cfg.Enabled || strings.TrimSpace(cfg.SendKey) == "" {
		return
	}
	if s.enc == nil {
		log.Printf("job balance-alert: no encryptor, cannot decrypt send key")
		return
	}
	sendKey, err := s.enc.Decrypt(cfg.SendKey)
	if err != nil {
		log.Printf("job balance-alert: decrypt send key: %v", err)
		return
	}

	now := time.Now()
	r24 := Range{From: now.UTC().Add(-24 * time.Hour), To: now.UTC()}
	items, err := s.urgent(ctx, r24, s.LoadSettings(), s.BalanceStale)
	if err != nil {
		log.Printf("job balance-alert: projection: %v", err)
		return
	}
	targets := filterAlertItems(items, cfg.HoursThreshold)
	if len(targets) == 0 {
		return
	}
	state := loadSilenceState(cfg.SilenceState)
	due := silenceDue(state, targets, time.Duration(cfg.SilenceHours)*time.Hour, now)
	if len(due) == 0 {
		return
	}

	sort.Slice(due, func(i, j int) bool { return due[i].Hours < due[j].Hours })
	title, desp := buildAlertMessage(due)
	sendCtx, cancel := context.WithTimeout(ctx, ServerChanSendTimeout)
	err = SendServerChan(sendCtx, sendKey, title, desp)
	cancel()
	if err != nil {
		log.Printf("job balance-alert: push: %v", err)
	} else {
		log.Printf("job balance-alert: pushed %d provider(s)", len(due))
	}

	for _, t := range due {
		state[t.ProviderID] = now
	}
	for id, at := range state {
		if now.Sub(at) > SilenceStateTTL {
			delete(state, id)
		}
	}
	next := cfg
	next.SilenceState = saveSilenceState(state)
	if _, err := s.SaveAlertSettings(next); err != nil {
		log.Printf("job balance-alert: save silence state: %v", err)
	}

	summary := title
	if err == nil {
		summary = "已推送：" + title
	} else {
		summary = "推送失败：" + title
	}
	notice := domain.Notice{
		Kind:    NoticeKindBalanceAlert,
		Source:  NoticeKindBalanceAlert,
		Summary: summary,
	}
	if raw, err := json.Marshal(map[string]any{
		"providers":  due,
		"push_error": errString(err),
	}); err == nil {
		notice.Payload = string(raw)
	}
	if err := s.db.WithContext(ctx).Create(&notice).Error; err != nil {
		log.Printf("job balance-alert: record notice: %v", err)
	}
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
