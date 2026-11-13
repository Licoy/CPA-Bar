package view

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/licoy/cpa-bar/internal/cpa"
	"github.com/licoy/cpa-bar/internal/quota"
	"github.com/licoy/cpa-bar/internal/store"
)

// A cache belongs to one connection and mode, so responses started before
// a mode or connection change cannot publish into the current display.
func (a *App) quotaCurrent(q *quotaInstance) bool {
	s := a.Svc.Settings()
	return a.quotas[q.id] == q && q.passive == s.PassiveQuota && q.claudePassive == s.ClaudePassiveQuota
}

func passiveQuota(s store.Settings, cr cpa.Credential) bool {
	if quota.ProviderKey(cr) == "claude" {
		return s.ClaudePassiveQuota
	}
	return s.PassiveQuota
}

func (q *quotaInstance) passiveFor(cr cpa.Credential) bool {
	return passiveQuota(store.Settings{PassiveQuota: q.passive, ClaudePassiveQuota: q.claudePassive}, cr)
}

func (e *quotaEntry) observe(cr cpa.Credential) {
	res, at, err := quota.Passive(cr)
	identity, versionAt, metaErr := quota.PassiveObservationMeta(cr)
	if metaErr != nil {
		res, err = nil, metaErr
	}
	// The server version remains usable even when its signals are invalid;
	// compare server clocks only and keep the latest watermark on missing data.
	if !versionAt.IsZero() && versionAt.Before(e.observedAt) {
		return
	}
	observation := "observed:" + identity
	e.timing = claudeQuotaTiming(cr, strings.HasPrefix(identity, "usage:"))
	if e.observation == observation {
		return
	}
	e.observation = observation
	if !versionAt.IsZero() {
		e.observedAt = versionAt
	}
	e.res, e.at, e.err = res, at, err
	e.failures = 0
	e.provider = quota.Provider{}
	e.fromCPA = true
}

type quotaTiming struct {
	age  *float64
	next *time.Time
	err  error
}

// Only display metadata attached to an accepted server observation; the quota
// package remains responsible for selecting usage, normalized windows or headers.
func claudeQuotaTiming(cr cpa.Credential, usage bool) quotaTiming {
	if quota.ProviderKey(cr) != "claude" || len(cr.Raw) == 0 {
		return quotaTiming{}
	}
	var record struct {
		Subscription *struct {
			Age  *float64   `json:"usage_age_seconds"`
			Next *time.Time `json:"next_poll_at"`
		} `json:"claude_subscription"`
	}
	if err := json.Unmarshal(cr.Raw, &record); err != nil {
		return quotaTiming{err: err}
	}
	if record.Subscription == nil {
		return quotaTiming{}
	}
	sub := record.Subscription
	if sub.Age != nil && (math.IsNaN(*sub.Age) || math.IsInf(*sub.Age, 0) || *sub.Age < 0) {
		return quotaTiming{err: fmt.Errorf("claude_subscription.usage_age_seconds: %v", *sub.Age)}
	}
	if !usage {
		sub.Age = nil
	}
	if sub.Next != nil && !sub.Next.After(time.Unix(0, 0)) {
		sub.Next = nil
	}
	return quotaTiming{age: sub.Age, next: sub.Next}
}

func (e *quotaEntry) timingTexts() []string {
	if !e.fromCPA {
		return nil
	}
	var texts []string
	if e.timing.err != nil {
		return []string{fmt.Sprintf(T("CPA 时间字段无效：%v", "Invalid CPA timing field: %v"), e.timing.err)}
	}
	if e.timing.age != nil {
		texts = append(texts, fmt.Sprintf(T("CPA 缓存年龄 · %.0f 秒", "CPA cache age · %.0f seconds"), math.Floor(*e.timing.age)))
	}
	if e.timing.next != nil {
		texts = append(texts, T("下次可查询 · ", "Next query allowed · ")+e.timing.next.Local().Format("01/02 15:04:05"))
	}
	return texts
}

func (e *quotaEntry) sourceText() string {
	source := T("手动查询", "Manual query")
	if e.fromCPA {
		source = T("CPA 观测", "CPA observation")
	}
	if !e.at.IsZero() {
		source += " · " + e.at.Local().Format("01/02 15:04:05")
	}
	return source
}

// Fork timing is displayed together, without substituting local query times.
func (e *quotaEntry) timingView(c *ui.Context) {
	t := c.Theme()
	if !e.fromCPA || e.at.IsZero() || e.timing.err != nil || (e.timing.age == nil && e.timing.next == nil) {
		ui.Text(c, e.sourceText()).FontSize(11).TextColor(t.TextMuted).SingleLine()
		for _, text := range e.timingTexts() {
			ui.Text(c, text).FontSize(11).TextColor(t.Warning).MaxLines(2)
		}
		return
	}
	age, next := T("未知", "Unknown"), T("未知", "Unknown")
	if e.timing.age != nil {
		age = fmt.Sprintf(T("%.0f 秒", "%.0f seconds"), math.Floor(*e.timing.age))
	}
	if e.timing.next != nil {
		next = e.timing.next.Local().Format("01/02 15:04:05")
	}
	ui.Row(c).Gap(8).Tooltip(e.sourceText()).Children(func() {
		for _, item := range [][2]string{
			{T("最后更新时间", "Last updated"), e.at.Local().Format("01/02 15:04:05")},
			{T("缓存年龄", "Cache age"), age},
			{T("下次可查询时间", "Next query allowed"), next},
		} {
			ui.Column(c).Grow(1).Basis(0).Gap(2).Children(func() {
				ui.Text(c, item[0]).FontSize(11).TextColor(t.TextMuted).SingleLine()
				ui.Text(c, item[1]).FontSize(11).TextColor(t.TextMuted).SingleLine()
			})
		}
	})
}
