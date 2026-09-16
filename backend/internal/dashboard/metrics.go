package dashboard

import (
	"encoding/json"
	"sync"
	"time"

	"github.com/google/uuid"
)

type Clock func() time.Time

type Snapshot struct {
	SchemaVersion    int       `json:"schema_version"`
	InstanceID       string    `json:"instance_id"`
	Sequence         uint64    `json:"sequence"`
	ServerTime       time.Time `json:"server_time"`
	StartedAt        time.Time `json:"started_at"`
	WindowSeconds    int       `json:"window_seconds"`
	WindowComplete   bool      `json:"window_complete"`
	BusinessInflight int       `json:"business_inflight"`
	BusinessRPM      int       `json:"business_rpm"`
	UpstreamInflight int       `json:"upstream_inflight"`
	UpstreamRPM      int       `json:"upstream_rpm"`
}

type Heartbeat struct {
	InstanceID string    `json:"instance_id"`
	ServerTime time.Time `json:"server_time"`
}

type Token struct {
	once sync.Once
	end  func()
}

func (t *Token) End() {
	if t == nil {
		return
	}
	t.once.Do(func() {
		if t.end != nil {
			t.end()
		}
	})
}

type Subscriber struct {
	Ch   chan Snapshot
	Done chan struct{}
}

type minuteSample struct {
	Bucket   time.Time
	Family   string
	Integral float64
	Peak     int
}

type Metrics struct {
	mu                  sync.Mutex
	clock               Clock
	instanceID          string
	startedAt           time.Time
	seq                 uint64
	bizN                int
	upN                 int
	bizPeak             int
	upPeak              int
	bizLast             time.Time
	upLast              time.Time
	bizIntegral         float64
	upIntegral          float64
	bizMinute           time.Time
	upMinute            time.Time
	bizRPM              []time.Time
	upRPM               []time.Time
	dirty               bool
	subs                map[*Subscriber]struct{}
	subscriptionsClosed bool
	samples             []minuteSample
	stop                chan struct{}
	wg                  sync.WaitGroup
}

func NewMetrics(clock Clock) *Metrics {
	if clock == nil {
		clock = time.Now
	}
	now := clock()
	m := &Metrics{
		clock:      clock,
		instanceID: uuid.NewString(),
		startedAt:  now,
		bizLast:    now,
		upLast:     now,
		bizMinute:  now.UTC().Truncate(time.Minute),
		upMinute:   now.UTC().Truncate(time.Minute),
		subs:       map[*Subscriber]struct{}{},
		stop:       make(chan struct{}),
	}
	m.wg.Add(1)
	go m.loop()
	return m
}

func (m *Metrics) Stop() {
	if m == nil {
		return
	}
	select {
	case <-m.stop:
		return
	default:
		close(m.stop)
	}
	m.wg.Wait()
	m.CloseSubscriptions()
}

// CloseSubscriptions ends long-lived HTTP streams before the server drains.
// Request counters remain active until the final business handler finishes.
func (m *Metrics) CloseSubscriptions() {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.subscriptionsClosed = true
	for sub := range m.subs {
		close(sub.Done)
		delete(m.subs, sub)
	}
}

func (m *Metrics) InstanceID() string { return m.instanceID }

func (m *Metrics) now() time.Time {
	if m.clock != nil {
		return m.clock()
	}
	return time.Now()
}

func (m *Metrics) BeginBusiness() *Token {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	m.rollBizLocked(now)
	m.bizN++
	if m.bizN > m.bizPeak {
		m.bizPeak = m.bizN
	}
	m.bizRPM = append(m.bizRPM, now)
	m.dirty = true
	return &Token{end: func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		now := m.now()
		m.rollBizLocked(now)
		if m.bizN > 0 {
			m.bizN--
		}
		m.dirty = true
	}}
}

func (m *Metrics) BeginUpstream() *Token {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	m.rollUpLocked(now)
	m.upN++
	if m.upN > m.upPeak {
		m.upPeak = m.upN
	}
	m.dirty = true
	return &Token{end: func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		now := m.now()
		m.rollUpLocked(now)
		if m.upN > 0 {
			m.upN--
		}
		m.dirty = true
	}}
}

func (m *Metrics) MarkUpstreamHTTP() {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.upRPM = append(m.upRPM, m.now())
	m.dirty = true
}

func (m *Metrics) Snapshot() Snapshot {
	if m == nil {
		return Snapshot{SchemaVersion: 1, WindowSeconds: 60}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.snapshotLocked(m.now())
}

func (m *Metrics) TakeSamples() []minuteSample {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	out := m.samples
	m.samples = nil
	return out
}

func (m *Metrics) Subscribe() *Subscriber {
	sub := &Subscriber{Ch: make(chan Snapshot, 1), Done: make(chan struct{})}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.subscriptionsClosed {
		close(sub.Done)
		return sub
	}
	sub.Ch <- m.snapshotLocked(m.now())
	m.subs[sub] = struct{}{}
	return sub
}

func (m *Metrics) Unsubscribe(sub *Subscriber) {
	if m == nil || sub == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.subs[sub]; ok {
		delete(m.subs, sub)
		close(sub.Done)
	}
}

func (m *Metrics) loop() {
	defer m.wg.Done()
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	hb := time.NewTicker(15 * time.Second)
	defer hb.Stop()
	for {
		select {
		case <-m.stop:
			return
		case <-tick.C:
			m.flush(false)
		case <-hb.C:
			m.flush(true)
		}
	}
}

func (m *Metrics) flush(force bool) {
	m.mu.Lock()
	now := m.now()
	m.rollBizLocked(now)
	m.rollUpLocked(now)
	m.pruneRPMLocked(now)
	if !m.dirty && !force {
		m.mu.Unlock()
		return
	}
	m.dirty = false
	m.seq++
	snap := m.snapshotLocked(now)
	subs := make([]*Subscriber, 0, len(m.subs))
	for s := range m.subs {
		subs = append(subs, s)
	}
	m.mu.Unlock()
	for _, s := range subs {
		select {
		case s.Ch <- snap:
		default:
			select {
			case <-s.Ch:
			default:
			}
			select {
			case s.Ch <- snap:
			default:
			}
		}
	}
}

func (m *Metrics) snapshotLocked(now time.Time) Snapshot {
	m.pruneRPMLocked(now)
	return Snapshot{
		SchemaVersion:    1,
		InstanceID:       m.instanceID,
		Sequence:         m.seq,
		ServerTime:       now.UTC(),
		StartedAt:        m.startedAt.UTC(),
		WindowSeconds:    60,
		WindowComplete:   now.Sub(m.startedAt) >= 60*time.Second,
		BusinessInflight: m.bizN,
		BusinessRPM:      len(m.bizRPM),
		UpstreamInflight: m.upN,
		UpstreamRPM:      len(m.upRPM),
	}
}

func (m *Metrics) pruneRPMLocked(now time.Time) {
	biz, up := len(m.bizRPM), len(m.upRPM)
	cut := now.Add(-60 * time.Second)
	m.bizRPM = pruneTimes(m.bizRPM, cut)
	m.upRPM = pruneTimes(m.upRPM, cut)
	if biz != len(m.bizRPM) || up != len(m.upRPM) {
		m.dirty = true
	}
}

func (m *Metrics) rollBizLocked(now time.Time) {
	bucket := now.UTC().Truncate(time.Minute)
	if !m.bizLast.IsZero() {
		m.bizIntegral += float64(m.bizN) * now.Sub(m.bizLast).Seconds()
	}
	if bucket != m.bizMinute {
		m.samples = append(m.samples, minuteSample{Bucket: m.bizMinute, Family: FamilyRequest, Integral: m.bizIntegral, Peak: m.bizPeak})
		m.bizIntegral = 0
		m.bizPeak = m.bizN
		m.bizMinute = bucket
	}
	if m.bizN > m.bizPeak {
		m.bizPeak = m.bizN
	}
	m.bizLast = now
}

func (m *Metrics) rollUpLocked(now time.Time) {
	bucket := now.UTC().Truncate(time.Minute)
	if !m.upLast.IsZero() {
		m.upIntegral += float64(m.upN) * now.Sub(m.upLast).Seconds()
	}
	if bucket != m.upMinute {
		m.samples = append(m.samples, minuteSample{Bucket: m.upMinute, Family: FamilyAttempt, Integral: m.upIntegral, Peak: m.upPeak})
		m.upIntegral = 0
		m.upPeak = m.upN
		m.upMinute = bucket
	}
	if m.upN > m.upPeak {
		m.upPeak = m.upN
	}
	m.upLast = now
}

func pruneTimes(in []time.Time, cut time.Time) []time.Time {
	if len(in) == 0 {
		return in
	}
	i := 0
	for i < len(in) && !in[i].After(cut) {
		i++
	}
	if i == 0 {
		return in
	}
	return append([]time.Time(nil), in[i:]...)
}

func encodeSSE(event string, v any) []byte {
	b, _ := json.Marshal(v)
	out := make([]byte, 0, len(event)+len(b)+16)
	out = append(out, "event: "...)
	out = append(out, event...)
	out = append(out, "\ndata: "...)
	out = append(out, b...)
	out = append(out, "\n\n"...)
	return out
}
