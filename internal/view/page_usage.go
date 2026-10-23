package view

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/licoy/cpa-bar/internal/usage"
)

func init() {
	RegisterPage(Page{
		ID:       "usage",
		Order:    5,
		Group:    GroupMonitor,
		Title:    func() string { return T("用量", "Usage") },
		Instance: true,
		View:     usagePage,
	})
	pageLooks["usage"] = pageLook{icChart, TintOrange}
}

// usagePageSize is how many requests the detail table shows per page.
const usagePageSize = 50

// usageRange is a choice of the time range.
type usageRange struct {
	zh, en string
	// dur is the span back from now; zero means since midnight.
	dur  time.Duration
	step time.Duration
}

var usageRanges = [...]usageRange{
	{"最近 4 小时", "Last 4 hours", 4 * time.Hour, 10 * time.Minute},
	{"最近 24 小时", "Last 24 hours", 24 * time.Hour, time.Hour},
	{"今天", "Today", 0, time.Hour},
	{"最近 7 天", "Last 7 days", 7 * 24 * time.Hour, 6 * time.Hour},
	{"最近 30 天", "Last 30 days", 30 * 24 * time.Hour, 24 * time.Hour},
}

const usageDefaultRange = 1

// usageResults are the choices of the outcome filter, in the order of
// usage.Result.
func usageResultLabels() []string {
	return []string{T("全部结果", "All results"), T("成功", "Success"), T("失败", "Failed"), T("已取消", "Canceled")}
}

// usageFilterSig is the choice of the filters, which the page of the table
// starts over with.
type usageFilterSig struct {
	inst                    string
	rng                     int
	model, provider, source string
	result                  int
}

// usageState is the page's state for an instance.
type usageState struct {
	rng                     int
	model, provider, source string // "" is all
	result                  int
	page                    int

	enableAsk, clearAsk bool
	enable              Task
	clearErr            error

	// The figures, kept until the records, the filter or the minute change.
	sumOK   bool
	fsig    usageFilterSig
	ver     uint64
	minute  int64
	from    time.Time
	to      time.Time
	sum     usage.Summary
	rows    []usage.Record
	rowsFor int // the page rows were read for
	total   int
}

func newUsageState() *usageState { return &usageState{rng: usageDefaultRange} }

// filter is the records the page selects at the minute now.
func (st *usageState) filter(now time.Time) usage.Filter {
	minute := now.Truncate(time.Minute)
	r := usageRanges[st.rng]
	from := minute.Add(-r.dur)
	if r.dur == 0 {
		y, m, d := minute.Date()
		from = time.Date(y, m, d, 0, 0, 0, 0, minute.Location())
	}
	return usage.Filter{
		From:     from,
		Model:    st.model,
		Provider: st.provider,
		Source:   st.source,
		Result:   usage.Result(st.result),
	}
}

// load brings the figures up to date. It reads the records only when they,
// the filter or the minute changed; one read computes the summary and the
// page of the table together.
func (st *usageState) load(a *App, id string, now time.Time) {
	for range 2 {
		f := st.filter(now)
		fsig := usageFilterSig{id, st.rng, st.model, st.provider, st.source, st.result}
		ver, minute := a.Usage.Version(), now.Truncate(time.Minute).Unix()
		needSum := !st.sumOK || fsig != st.fsig || ver != st.ver || minute != st.minute
		if needSum && fsig != st.fsig {
			st.page = 0 // a new filter starts at the first page
		}
		if !needSum && st.rowsFor == st.page {
			return
		}
		step := usageRanges[st.rng].step
		a.Usage.Read(id, func(recs []usage.Record) {
			if needSum {
				st.sum = usage.Summarize(recs, f, step, now)
			}
			st.rows, st.total = usage.Recent(recs, f, st.page*usagePageSize, usagePageSize)
		})
		st.fsig, st.ver, st.minute, st.sumOK, st.rowsFor = fsig, ver, minute, true, st.page
		st.from, st.to = f.From, now
		if st.total > 0 && st.page*usagePageSize >= st.total {
			// Records were cleared or aged out under the page: back to the
			// last one.
			st.page = (st.total - 1) / usagePageSize
			continue
		}
		// A choice the range no longer holds selects nothing: back to all.
		o := st.sum.Options
		reset := false
		if st.model != "" && !slices.Contains(o.Models, st.model) {
			st.model, reset = "", true
		}
		if st.provider != "" && !slices.ContainsFunc(o.Providers, func(p string) bool { return strings.EqualFold(p, st.provider) }) {
			st.provider, reset = "", true
		}
		if st.source != "" && !slices.Contains(o.Sources, st.source) {
			st.source, reset = "", true
		}
		if !reset {
			return
		}
		st.sumOK = false
	}
}

func usagePage(c *ui.Context, env *PageEnv) {
	a := env.App
	t := c.Theme()
	st := State(env, "usage", newUsageState)

	PageHeader(c, T("用量", "Usage"),
		T("记录来自 CPA 的用量流，仅在本机保存 30 天。", "Records come from CPA's usage stream and are kept on this computer for 30 days."),
		func() {
			have := st.sum.Requests > 0 || st.total > 0
			if a.Usage != nil && logsDangerPill(c, icTrash, T("清除本地记录", "Clear Local Records"), !have).Clicked() {
				st.clearAsk = true
			}
		})
	if a.Usage == nil {
		EmptyState(c, T("用量不可用", "Usage is unavailable"), T("本次运行没有启用用量记录。", "Usage records are not enabled in this run."))
		return
	}
	id := env.InstanceID
	now := c.Now()
	// The range slides with the clock: come back at the next minute.
	c.After(now.Truncate(time.Minute).Add(time.Minute).Sub(now) + 50*time.Millisecond)

	usageCollector(c, env, st)
	usageDialogs(c, env, st)
	TaskStatus(c, &st.enable, T("正在开启用量统计…", "Turning usage statistics on…"), T("已开启用量统计", "Usage statistics are on"))
	ErrorBanner(c, st.clearErr)

	st.load(a, id, now)
	usageFilters(c, st)
	s := st.sum
	usageStats(c, st, s)

	if s.Requests == 0 {
		Panel(c).Children(func() {
			msg := T("所选范围内没有用量记录。", "No usage records in the selected range.")
			if st.model != "" || st.provider != "" || st.source != "" || st.result != 0 {
				msg = T("没有符合筛选条件的记录。", "No records match the filters.")
			}
			ui.Text(c, msg).FontSize(13).TextColor(t.TextMuted)
		})
		return
	}
	usageTrend(c, st, s)
	usageComposition(c, s)
	w, _ := c.Size()
	ui.Grid(c).Columns(ResponsiveColumns(w-sidebarWidth-48, 340, 2)).Gap(14).Children(func() {
		usageGroupPanel(c, "models", icChart, TintBlue, T("模型", "Models"), s.Models, func(g usage.Group) string { return g.Key })
		usageGroupPanel(c, "providers", icCloud, TintIndigo, T("服务商", "Providers"), s.Providers, func(g usage.Group) string {
			if g.Key == "unknown" {
				return T("未知", "Unknown")
			}
			return providerLabel(g.Key)
		})
		usageGroupPanel(c, "sources", icUsers, TintGreen, T("账号", "Accounts"), s.Sources, func(g usage.Group) string { return PrivName(g.Key) })
		usageGroupPanel(c, "keys", icKey, TintYellow, T("API 密钥", "API Keys"), s.Keys, func(g usage.Group) string {
			if g.Label != "" {
				return g.Label
			}
			return g.Key
		})
	})
	keys := State(env, "keys", newKeysState)
	if !keys.started {
		keys.reload(env)
	}
	ErrorBanner(c, keys.load.Err)
	st.table(c, keys.keys, w-sidebarWidth-48)
}

// usageCollector shows where the instance's stream is, and warns when the
// server records no usage.
func usageCollector(c *ui.Context, env *PageEnv, st *usageState) {
	t := c.Theme()
	a := env.App
	status := a.Usage.Status(env.InstanceID)
	label, col := usageStateLabel(t, status)
	detail := usageStreamDetail(status, env.Now)
	if status.ManagerPlus {
		detail = usagePlusDetail(status, env.Now)
	} else if env.Snap.Profile.ManagerPlus() && a.Usage.Enabled() {
		// Without the admin key nothing is collected: the stream would
		// take the records Manager-Plus collects.
		if _, err := a.Svc.ManagerPlus(env.InstanceID); err != nil {
			detail = T("无法读取 CPA-Manager-Plus：", "Cannot read CPA-Manager-Plus: ") + ErrorText(err)
		}
	}
	ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
		Pill(c, label, col)
		if detail != "" {
			ui.Text(c, detail).FontSize(12).TextColor(t.TextMuted).Grow(1).Basis(0).MaxLines(2)
		}
	})
	if status.Err != nil && status.State != usage.StateLive && status.State != usage.StateOff {
		ui.Text(c, ErrorText(status.Err)).FontSize(11).TextColor(t.TextMuted).MaxLines(2).Selectable()
	}

	if status.StatsEnabled != nil && !*status.StatsEnabled {
		ui.Row(c).Gap(10).Padding(10, 12).Radius(12).Background(t.Warning.Alpha(0.12)).AlignItems(ui.Center).Children(func() {
			ui.Icon(c, icAlert).FontSize(15).TextColor(t.Warning)
			ui.Text(c, T("CPA 未开启 usage-statistics-enabled，不会产生用量记录。", "CPA has usage-statistics-enabled off, so no usage records are produced.")).Grow(1).Basis(0).FontSize(12)
			if PillButton(c, icActivity, T("开启用量统计", "Turn On Usage Statistics"), env.Client == nil || st.enable.Running).Clicked() {
				st.enableAsk = true
			}
		})
	}
}

// usageDialogs are the confirmations of the page.
func usageDialogs(c *ui.Context, env *PageEnv, st *usageState) {
	a := env.App
	if st.enableAsk {
		msg := fmt.Sprintf(T("将在实例“%s”的服务器上写入 usage-statistics-enabled: true。写入只发送一次；若结果待确认，请先刷新确认，不要重复提交。",
			"This writes usage-statistics-enabled: true on the server of “%s”. The write is sent once; if its result is unknown, refresh to confirm before trying again."), env.InstanceName())
		if ui.AlertDialog(c.Key("usage-enable"), &st.enableAsk, T("开启用量统计？", "Turn on usage statistics?"), msg, T("取消", "Cancel"), T("确认开启", "Confirm")) == 1 && env.Client != nil {
			id, cl := env.InstanceID, env.Client
			env.Run(&st.enable, func(ctx context.Context) error { return a.Usage.EnableStats(ctx, id, cl) }, nil)
		}
	}
	if st.clearAsk {
		msg := fmt.Sprintf(T("将删除实例“%s”保存在本机的全部用量记录。服务器上的数据不受影响，此操作不可恢复。",
			"This deletes every usage record of “%s” kept on this computer. The server is not affected. It cannot be undone."), env.InstanceName())
		if ui.AlertDialog(c.Key("usage-clear"), &st.clearAsk, T("清除本地用量记录？", "Clear local usage records?"), msg, T("取消", "Cancel"), T("确认清除", "Confirm Clear")) == 1 {
			st.clearErr = a.Usage.Clear(env.InstanceID)
			st.sumOK = false
			st.page = 0
		}
	}
}

// usageChoice is an entry of a filter menu.
type usageChoice struct{ label, id string }

// usageSelect is a filter menu over choices, plus the one for all. It
// reports a change of *cur, which holds the id.
func usageSelect(c *ui.Context, key, name, all string, cur *string, choices []usageChoice) bool {
	labels := []string{all}
	var kept []usageChoice
	for _, ch := range choices {
		l := ch.label
		for n := 2; slices.Contains(labels, l); n++ {
			l = ch.label + " #" + strconv.Itoa(n) // masked names may collide
		}
		labels = append(labels, l)
		kept = append(kept, usageChoice{l, ch.id})
	}
	sel := all
	for _, ch := range kept {
		if ch.id == *cur {
			sel = ch.label
		}
	}
	if !ui.Select(c.Key(key), &sel, labels).Label(name).Grow(1).Basis(0).Changed() {
		return false
	}
	*cur = ""
	for _, ch := range kept {
		if ch.label == sel {
			*cur = ch.id
		}
	}
	return true
}

func usageFilters(c *ui.Context, st *usageState) {
	o := st.sum.Options
	ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
		labels := make([]string, len(usageRanges))
		for i, r := range usageRanges {
			labels[i] = T(r.zh, r.en)
		}
		PillTabs(c.Key("usage-range"), &st.rng, labels...)
	})
	ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
		models := make([]usageChoice, len(o.Models))
		for i, m := range o.Models {
			models[i] = usageChoice{m, m}
		}
		usageSelect(c, "usage-model", T("模型", "Model"), T("全部模型", "All models"), &st.model, models)
		var providers []usageChoice
		for _, p := range o.Providers {
			providers = append(providers, usageChoice{providerLabel(p), p})
		}
		usageSelect(c, "usage-provider", T("服务商", "Provider"), T("全部服务商", "All providers"), &st.provider, providers)
		var sources []usageChoice
		for _, s := range o.Sources {
			sources = append(sources, usageChoice{PrivName(s), s})
		}
		usageSelect(c, "usage-source", T("账号", "Account"), T("全部账号", "All accounts"), &st.source, sources)
		results := usageResultLabels()
		cur := results[st.result]
		if ui.Select(c.Key("usage-result"), &cur, results).Label(T("结果", "Result")).Grow(1).Basis(0).Changed() {
			st.result = max(0, slices.Index(results, cur))
		}
	})
}

func usageStats(c *ui.Context, st *usageState, s usage.Summary) {
	t := c.Theme()
	dash := "—"
	pct := func(v float64, ok bool) string {
		if !ok {
			return dash
		}
		return fmt.Sprintf("%.1f%%", v*100)
	}
	rate, rateOK := s.SuccessRate()
	cache, cacheOK := s.CacheRate()
	lat, latOK := s.AvgLatency()
	ttft, ttftOK := s.AvgTTFT()
	tps, tpsOK := s.TPS()
	latency, first, tpsText := dash, dash, dash
	if latOK {
		latency = usageDur(lat.Milliseconds())
	}
	if ttftOK {
		first = usageDur(ttft.Milliseconds())
	}
	if tpsOK {
		tpsText = fmt.Sprintf("%.1f", tps)
	}
	rateCol := t.Text
	if rateOK && rate < 0.9 {
		rateCol = t.Warning
	}
	w, _ := c.Size()
	ui.Grid(c).Columns(ResponsiveColumns(w-sidebarWidth-48, 200, 3)).Gap(14).Children(func() {
		usageStat(c, T("请求数", "Requests"), usageNum(int64(s.Requests)),
			fmt.Sprintf(T("成功 %d · 失败 %d · 取消 %d", "%d ok · %d failed · %d canceled"), s.Success, s.Failed, s.Canceled), t.Text)
		usageStat(c, T("成功率", "Success rate"), pct(rate, rateOK), T("不计入已取消的请求", "Canceled requests left out"), rateCol)
		usageStat(c, T("Token 总量", "Total tokens"), compactNum(s.Tokens.Total),
			fmt.Sprintf(T("输入 %s · 输出 %s", "in %s · out %s"), compactNum(s.Tokens.Input), compactNum(s.Tokens.Output)), t.Text)
		usageStat(c, T("缓存命中率", "Cache hit rate"), pct(cache, cacheOK),
			fmt.Sprintf(T("读取 %s · 写入 %s", "read %s · write %s"), compactNum(s.Tokens.CacheRead), compactNum(s.Tokens.CacheCreate)), t.Text)
		usageStat(c, T("平均延迟", "Avg latency"), latency, fmt.Sprintf(T("平均首字 %s", "avg first token %s"), first), t.Text)
		usageStat(c, "TPS / RPM", tpsText, fmt.Sprintf(T("RPM %.1f 次/分钟", "RPM %.1f req/min"), s.RPM(st.from, st.to)), t.Text)
	})
}

// usageStat is a card with a figure, its caption and a line of detail.
func usageStat(c *ui.Context, caption, value, detail string, col ui.Color) {
	t := c.Theme()
	ui.Column(c).Gap(3).Padding(12, 14).Radius(14).Background(pal(t).Card).Children(func() {
		ui.Text(c, caption).FontSize(12).TextColor(t.TextMuted).SingleLine()
		ui.Text(c, value).FontSize(24).FontWeight(700).TextColor(col).FontFeatures("tnum").SingleLine()
		ui.Text(c, detail).FontSize(11).TextColor(t.TextMuted).SingleLine()
	})
}

// usageSeries are the colors of the models of the trend, the last for the
// rest.
var usageSeries = [...]Tint{TintBlue, TintTeal, TintGreen, TintOrange, TintPurple, TintPink}

func usageSeriesColor(t *ui.Theme, i, top int) ui.Color {
	if i >= top || i >= len(usageSeries) {
		return TintGray.C(t)
	}
	return usageSeries[i].C(t)
}

func usageStepLabel(step time.Duration) string {
	switch {
	case step >= 24*time.Hour:
		return T("每天", "per day")
	case step >= time.Hour:
		return fmt.Sprintf(T("每 %d 小时", "per %d h"), int(step/time.Hour))
	}
	return fmt.Sprintf(T("每 %d 分钟", "per %d min"), int(step/time.Minute))
}

func usageBucketTime(tm time.Time, step time.Duration) string {
	if step >= 24*time.Hour {
		return tm.Local().Format("01-02")
	}
	return tm.Local().Format("01-02 15:04")
}

// usageTrend draws the tokens of each slot, split by model, and the
// requests and failures under it.
func usageTrend(c *ui.Context, st *usageState, s usage.Summary) {
	t := c.Theme()
	step := usageRanges[st.rng].step
	bs := s.Buckets
	var peak int64
	var peakReq int
	for _, b := range bs {
		peak = max(peak, b.Tokens)
		peakReq = max(peakReq, b.Requests)
	}
	top := len(s.TopModels)
	Panel(c).Children(func() {
		PanelTitle(c, icChart, TintBlue, T("Token 趋势", "Token Trend"), usageStepLabel(step), false)
		const h = 150
		ui.Row(c).Gap(8).Children(func() {
			ui.Column(c).Height(h).Width(44).Children(func() {
				ui.Text(c, compactNum(peak)).FontSize(11).TextColor(t.TextMuted).FontFeatures("tnum").SingleLine().TextAlign(ui.End)
				ui.Box(c).Grow(1)
				ui.Text(c, "0").FontSize(11).TextColor(t.TextMuted).FontFeatures("tnum").SingleLine().TextAlign(ui.End)
			})
			line := pal(t).Line
			ui.Box(c.Key("usage-trend")).Grow(1).Basis(0).Height(h).Draw(func(p *ui.Painter, r ui.Rect) {
				for _, f := range []float32{0, 0.5, 1} {
					y := r.Y + r.H - 1 - f*(r.H-2)
					p.Line(r.X, y, r.X+r.W, y, 1, line)
				}
				n := len(bs)
				if n == 0 || peak == 0 {
					return
				}
				slot := r.W / float32(n)
				gap := float32(2)
				if slot < 6 {
					gap = 1
				}
				bw := max(1, slot-gap)
				for i, b := range bs {
					x := r.X + float32(i)*slot + (slot-bw)/2
					y := r.Y + r.H - 1
					for j, v := range b.ByModel {
						if v <= 0 {
							continue
						}
						sh := max(1, float32(v)/float32(peak)*(r.H-2))
						y -= sh
						p.Fill(ui.Rect{X: x, Y: y, W: bw, H: sh}, usageSeriesColor(t, j, top), min(1.5, bw/2))
					}
				}
			})
		})
		if len(bs) > 0 {
			ui.Row(c).Padding(0, 0, 0, 52).Children(func() {
				ui.Text(c, usageBucketTime(bs[0].Start, step)).FontSize(11).TextColor(t.TextMuted).SingleLine().Grow(1)
				ui.Text(c, usageBucketTime(bs[len(bs)-1].Start, step)).FontSize(11).TextColor(t.TextMuted).SingleLine()
			})
		}
		ui.Row(c).Gap(14).Children(func() {
			ui.Row(c).Gap(14).Grow(1).Basis(0).Wrap().Children(func() {
				for i, m := range s.TopModels {
					usageLegend(c, fmt.Sprint("m", i), usageSeriesColor(t, i, top), m)
				}
				if top < len(s.Models) {
					usageLegend(c, "other", usageSeriesColor(t, top, top), T("其他", "Other"))
				}
			})
		})

		// Requests and failures.
		ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
			ui.Text(c, T("请求 / 失败", "Requests / failed")).FontSize(12).FontWeight(600).TextColor(t.TextMuted).Grow(1)
			usageLegend(c, "req", TintBlue.C(t), T("请求", "Requests"))
			usageLegend(c, "fail", TintRed.C(t), T("失败", "Failed"))
			ui.Text(c, fmt.Sprintf(T("峰值 %d 次", "peak %d"), peakReq)).FontSize(11).TextColor(t.TextMuted).FontFeatures("tnum")
		})
		blue, red, base := TintBlue.C(t), TintRed.C(t), TintBlue.C(t).Alpha(0.10)
		ui.Box(c.Key("usage-requests")).Height(44).Radius(8).Background(base).Draw(func(p *ui.Painter, r ui.Rect) {
			n := len(bs)
			if n == 0 || peakReq == 0 {
				return
			}
			slot := r.W / float32(n)
			gap := float32(2)
			if slot < 6 {
				gap = 1
			}
			bw := max(1, slot-gap)
			for i, b := range bs {
				if b.Requests == 0 {
					continue
				}
				x := r.X + float32(i)*slot + (slot-bw)/2
				hh := max(2, float32(b.Requests)/float32(peakReq)*(r.H-4))
				p.Fill(ui.Rect{X: x, Y: r.Y + r.H - 2 - hh, W: bw, H: hh}, blue, min(1.5, bw/2))
				if b.Failed > 0 {
					fh := max(2, float32(b.Failed)/float32(peakReq)*(r.H-4))
					p.Fill(ui.Rect{X: x, Y: r.Y + r.H - 2 - fh, W: bw, H: fh}, red, min(1.5, bw/2))
				}
			}
		})
	})
}

// usageLegend is a color dot and a name.
func usageLegend(c *ui.Context, key string, col ui.Color, name string) {
	ui.Row(c.Key(key)).Gap(6).AlignItems(ui.Center).Children(func() {
		ui.Box(c).Size(8, 8).Radius(2).Background(col)
		ui.Text(c, name).FontSize(12).SingleLine()
	})
}

// usageComposition splits the tokens into what was read from the cache,
// written to it, plain input and output.
func usageComposition(c *ui.Context, s usage.Summary) {
	t := c.Theme()
	k := s.Tokens
	plain := max(0, k.Input-k.CacheRead-k.CacheCreate)
	output := k.Output
	type part struct {
		name   string
		v      int64
		col    ui.Color
		detail string
	}
	parts := []part{
		{T("输入", "Input"), plain, TintBlue.C(t), ""},
		{T("缓存读取", "Cache read"), k.CacheRead, TintGreen.C(t), ""},
		{T("缓存写入", "Cache write"), k.CacheCreate, TintOrange.C(t), ""},
		{T("输出", "Output"), output, TintPurple.C(t), fmt.Sprintf(T("其中思考 %s", "of which reasoning %s"), compactNum(k.Reasoning))},
	}
	var total int64
	for _, p := range parts {
		total += p.v
	}
	Panel(c).Children(func() {
		PanelTitle(c, icGauge, TintPurple, T("Token 构成", "Token Breakdown"), compactNum(k.Total)+" Token", false)
		ui.Box(c.Key("usage-mix")).Height(12).Radius(6).Background(pal(t).Line).Draw(func(p *ui.Painter, r ui.Rect) {
			if total == 0 {
				return
			}
			p.Clip(r, 6, func() {
				x := r.X
				for _, pt := range parts {
					if pt.v <= 0 {
						continue
					}
					w := float32(pt.v) / float32(total) * r.W
					p.Fill(ui.Rect{X: x, Y: r.Y, W: w, H: r.H}, pt.col, 0)
					x += w
				}
			})
		})
		ui.Grid(c).Columns(4).Gap(14).Children(func() {
			for i, pt := range parts {
				ui.Column(c.Key(fmt.Sprint("mix", i))).Gap(2).Children(func() {
					ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
						ui.Box(c).Size(8, 8).Radius(2).Background(pt.col)
						ui.Text(c, pt.name).FontSize(12).TextColor(t.TextMuted).SingleLine()
					})
					share := "—"
					if total > 0 {
						share = fmt.Sprintf("%.1f%%", float64(pt.v)*100/float64(total))
					}
					ui.Text(c, usageNum(pt.v)).FontSize(15).FontWeight(600).FontFeatures("tnum").SingleLine()
					sub := share
					if pt.detail != "" {
						sub += " · " + pt.detail
					}
					ui.Text(c, sub).FontSize(11).TextColor(t.TextMuted).FontFeatures("tnum").SingleLine()
				})
			}
		})
	})
}

// usageGroupPanel lists the biggest of a kind of group, with a bar to show
// its share.
// A group of providers or of accounts has its provider's logo.
func usageGroupPanel(c *ui.Context, key string, icon *ui.SVG, k Tint, title string, groups []usage.Group, name func(usage.Group) string) {
	t := c.Theme()
	const most = 8
	byTokens := false
	var peak int64
	for _, g := range groups {
		if g.Tokens.Total > 0 {
			byTokens = true
		}
	}
	for _, g := range groups {
		peak = max(peak, pick(byTokens, g.Tokens.Total, int64(g.Requests)))
	}
	Panel(c.Key("usage-" + key)).Children(func() {
		PanelTitle(c, icon, k, title, fmt.Sprintf(T("%d 项", "%d"), len(groups)), false)
		if len(groups) == 0 {
			ui.Text(c, T("暂无数据。", "No data.")).FontSize(13).TextColor(t.TextMuted)
			return
		}
		ui.Column(c).Gap(10).Children(func() {
			for i, g := range groups {
				if i == most {
					ui.Text(c, fmt.Sprintf(T("还有 %d 项", "%d more"), len(groups)-most)).FontSize(12).TextColor(t.TextMuted)
					break
				}
				v := pick(byTokens, g.Tokens.Total, int64(g.Requests))
				ui.Column(c.Key(fmt.Sprint("g", i))).Gap(4).Children(func() {
					ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
						switch key {
						case "providers":
							if g.Key != "unknown" {
								ProviderIcon(c, g.Key, 16)
							}
						case "sources":
							if g.Provider != "" {
								ProviderIcon(c, g.Provider, 16)
							}
						}
						ui.Text(c, name(g)).FontSize(13).FontWeight(500).SingleLine().Grow(1).Basis(0)
						ui.Text(c, compactNum(g.Tokens.Total)+" Token").FontSize(12).FontWeight(600).FontFeatures("tnum").SingleLine()
					})
					Bar(c, float64(v)/float64(max(peak, 1)), k.C(t))
					ui.Row(c).Gap(8).Children(func() {
						ui.Text(c, fmt.Sprintf(T("%s 次请求", "%s requests"), usageNum(int64(g.Requests)))).FontSize(11).TextColor(t.TextMuted).FontFeatures("tnum").SingleLine()
						if g.Failed > 0 {
							ui.Text(c, fmt.Sprintf(T("%s 次失败", "%s failed"), usageNum(int64(g.Failed)))).FontSize(11).TextColor(t.Danger).FontFeatures("tnum").SingleLine()
						}
					})
				})
			}
		})
	})
}

// usageCols are the widths of the table's columns; a zero width grows by
// its weight.
var usageCols = [...]struct{ w, grow float32 }{
	{w: 60},   // time
	{grow: 3}, // model
	{w: 108},  // provider
	{grow: 2}, // account
	{w: 80},   // client key
	{w: 62},   // result
	{w: 92},   // tokens
	{w: 52},   // cache
	{w: 76},   // latency
}

func usageCell(c *ui.Context, i int, fn func()) {
	col := ui.Column(c).Gap(1)
	if usageCols[i].w > 0 {
		col.Width(usageCols[i].w)
	} else {
		col.Grow(usageCols[i].grow).Basis(0)
	}
	if i == 1 {
		col.MaxWidth(160)
	}
	col.Children(fn)
}

// table lists the requests newest first, filling the available page width.
func (st *usageState) table(c *ui.Context, keys []string, width float32) {
	t := c.Theme()
	rows := st.rows
	from := st.page*usagePageSize + 1
	to := st.page*usagePageSize + len(rows)
	Panel(c).Children(func() {
		PanelTitle(c, icLogs, TintTeal, T("请求明细", "Requests"), fmt.Sprintf(T("共 %s 条", "%s in all"), usageNum(int64(st.total))), false)
		if len(rows) == 0 {
			ui.Text(c, T("暂无记录。", "No records.")).FontSize(13).TextColor(t.TextMuted)
			return
		}
		ui.ScrollHorizontal(c).Children(func() {
			// A horizontal scroller measures children with unbounded width,
			// so use its viewport width explicitly, excluding Panel's padding.
			ui.Column(c).Width(max(744, width-36)).Shrink(0).Gap(14).Children(func() {
				heads := []string{T("时间", "Time"), T("模型", "Model"), T("服务商", "Provider"), T("账号", "Account"), "Key", T("结果", "Result"), "Token", T("缓存", "Cache"), T("耗时", "Time taken")}
				ui.Row(c).Gap(8).Padding(0, 4).Children(func() {
					for i, h := range heads {
						usageCell(c, i, func() { ui.Text(c, h).FontSize(11).FontWeight(600).TextColor(t.TextMuted).SingleLine() })
					}
				})
				ui.Column(c).Children(func() {
					prevDay := ""
					today := c.Now().Local().Format("01-02")
					for i, r := range rows {
						day := r.Time.Local().Format("01-02")
						showDay := day != prevDay && (i > 0 || day != today)
						prevDay = day
						if !showDay {
							day = ""
						}
						ui.Box(c.Key(fmt.Sprint("rule", i))).Height(1).Background(pal(t).Line)
						ui.Column(c.Key(fmt.Sprint("req", i))).Gap(2).Padding(7, 4).Children(func() {
							usageRow(c, r, day, r.ClientKey(keys))
							if r.Failed && !r.Canceled || r.Canceled && r.Error != "" {
								line := ""
								if r.Status > 0 {
									line = fmt.Sprintf("HTTP %d", r.Status)
								}
								if e := PrivText(r.Error); e != "" {
									if line != "" {
										line += " · "
									}
									line += e
								}
								if line != "" {
									ui.Text(c, line).FontSize(11).TextColor(t.Danger).SingleLine().Selectable()
								}
							}
						})
					}
				})
			})
		})
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, fmt.Sprintf(T("第 %d–%d 条，共 %d 条", "%d–%d of %d"), from, to, st.total)).FontSize(12).TextColor(t.TextMuted).FontFeatures("tnum").Grow(1)
			if PillButton(c.Key("usage-prev"), nil, T("上一页", "Previous"), st.page == 0).Clicked() {
				st.page--
			}
			if PillButton(c.Key("usage-next"), nil, T("下一页", "Next"), to >= st.total).Clicked() {
				st.page++
			}
		})
	})
}

func usageRow(c *ui.Context, r usage.Record, day, key string) {
	t := c.Theme()
	ui.Row(c).Gap(8).Children(func() {
		usageCell(c, 0, func() {
			ui.Text(c, r.Time.Local().Format("15:04:05")).FontSize(12).FontFeatures("tnum").SingleLine()
			if day != "" {
				ui.Text(c, day).FontSize(11).TextColor(t.TextMuted).FontFeatures("tnum").SingleLine()
			}
		})
		usageCell(c, 1, func() {
			ui.Text(c, r.DisplayModel()).FontSize(12).FontWeight(500).SingleLine().Selectable()
			if r.Alias != "" && r.Model != "" {
				ui.Text(c, r.Model).FontSize(11).TextColor(t.TextMuted).SingleLine()
			}
		})
		usageCell(c, 2, func() {
			p := T("未知", "Unknown")
			if r.Provider != "" {
				p = providerLabel(r.Provider)
			}
			ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
				if r.Provider != "" {
					ProviderIcon(c, r.Provider, 14)
				}
				ui.Text(c, p).FontSize(12).MaxLines(2).Grow(1).Basis(0)
			})
		})
		usageCell(c, 3, func() {
			ui.Text(c, PrivName(r.Source)).FontSize(12).SingleLine()
		})
		usageCell(c, 4, func() {
			label, tip := r.KeyMask, T("完整 Key 不可用，密钥可能已删除", "Full key unavailable; it may have been deleted")
			if key != "" {
				label, tip = maskMiddle(key), key
			} else if label == "" {
				label = "—"
				if r.KeyID == "" {
					tip = T("此请求未记录 Key", "No key was recorded for this request")
				}
			}
			anchor := ui.Text(c, label).FontSize(12).SingleLine().Description(tip)
			if anchor.Hovered() {
				ui.Overlay(c, func() {
					ui.Box(c).Absolute().PassThrough().Role(ui.RoleTooltip).
						MaxWidth(320).Padding(5, 8).Radius(5).Background(t.Inverse).TextColor(t.InverseText).
						Shadow(0, 2, 8, 0, ui.RGBA(0, 0, 0, 0.2)).
						AttachTo(anchor, ui.AnchorTop, ui.AnchorBottom).Margin(0, 0, 6, 0).
						Children(func() { ui.Text(c, tip).FontSize(12) })
				})
			}
		})
		usageCell(c, 5, func() {
			switch {
			case r.Canceled:
				Pill(c, T("已取消", "Canceled"), t.TextMuted).AlignSelf(ui.Start)
			case r.Failed:
				Pill(c, T("失败", "Failed"), t.Danger).AlignSelf(ui.Start)
			default:
				Pill(c, T("成功", "OK"), t.Success).AlignSelf(ui.Start)
			}
		})
		usageCell(c, 6, func() {
			ui.Text(c, usageNum(r.Tokens.Total)).FontSize(12).FontWeight(500).FontFeatures("tnum").SingleLine()
			ui.Text(c, "↑"+compactNum(r.Tokens.Input)+" ↓"+compactNum(r.Tokens.Output)).FontSize(11).TextColor(t.TextMuted).FontFeatures("tnum").SingleLine()
		})
		usageCell(c, 7, func() {
			v := "—"
			if r.Tokens.CacheRead > 0 {
				v = compactNum(r.Tokens.CacheRead)
			}
			ui.Text(c, v).FontSize(12).FontFeatures("tnum").SingleLine()
		})
		usageCell(c, 8, func() {
			ui.Text(c, pick(r.LatencyMs > 0, usageDur(r.LatencyMs), "—")).FontSize(12).FontFeatures("tnum").SingleLine()
			if r.TTFTMs > 0 {
				ui.Text(c, T("首字 ", "TTFT ")+usageDur(r.TTFTMs)).FontSize(11).TextColor(t.TextMuted).FontFeatures("tnum").SingleLine()
			}
		})
	})
}

// usageNum writes a count with thousands separators: 1,234.
func usageNum(n int64) string {
	s := strconv.FormatInt(n, 10)
	neg := strings.HasPrefix(s, "-")
	if neg {
		s = s[1:]
	}
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	if neg {
		s = "-" + s
	}
	return s
}

// usageDur writes a duration in milliseconds: 820 ms, 1.2 s, 2m05s.
func usageDur(ms int64) string {
	switch {
	case ms < 1000:
		return strconv.FormatInt(ms, 10) + " ms"
	case ms < 60_000:
		return fmt.Sprintf("%.1f s", float64(ms)/1000)
	}
	return fmt.Sprintf("%dm%02ds", ms/60_000, ms/1000%60)
}

// usageStreamDetail says more of the state of the stream.
func usageStreamDetail(status usage.Status, now time.Time) string {
	var detail string
	switch status.State {
	case usage.StateLive:
		detail = fmt.Sprintf(T("本次已接收 %s 条", "%s received this session"), usageNum(int64(status.Received)))
		if !status.Last.IsZero() {
			detail += " · " + fmt.Sprintf(T("最近一条 %s", "last %s"), Ago(status.Last, now))
		}
	case usage.StateRetrying:
		detail = fmt.Sprintf(T("%s重连", "Reconnecting %s"), Until(status.Retry, now))
	case usage.StateRejected:
		detail = T("密钥被拒绝：不会自动重试，更新密钥后重连。", "The key was refused: it is not retried; update the key to reconnect.")
	case usage.StateHomeMode:
		detail = T("Home 模式下不提供用量流。", "Home mode has no usage stream.")
	case usage.StateUnsupported:
		detail = T("地址不支持用量流（可能经过只转发 HTTP 的反向代理）。", "This address does not answer the usage stream (perhaps an HTTP-only reverse proxy).")
	case usage.StateOff:
		detail = T("已关闭，可在“设置”中开启。", "Off; turn it on in Settings.")
	}
	return detail
}

// usagePlusDetail says more of the reads from CPA-Manager-Plus.
func usagePlusDetail(status usage.Status, now time.Time) string {
	switch status.State {
	case usage.StateLive:
		d := fmt.Sprintf(T("从 CPA-Manager-Plus 读取，含它保存的历史 · 本次新增 %s 条", "Read from CPA-Manager-Plus, its history included · %s new this session"), usageNum(int64(status.Received)))
		if !status.Synced.IsZero() {
			d += " · " + fmt.Sprintf(T("最近同步 %s", "synced %s"), Ago(status.Synced, now))
		}
		return d
	case usage.StateConnecting:
		return T("首次同步会读取最近 30 天的记录。", "The first sync reads the last 30 days.")
	case usage.StateRetrying:
		return fmt.Sprintf(T("%s重试", "Retrying %s"), Until(status.Retry, now))
	case usage.StateRejected:
		return T("管理员密钥被拒绝：不会自动重试，在“实例”中更新密钥后重连。", "The admin key was refused: it is not retried; update it on the Instances page to reconnect.")
	case usage.StateOff:
		return T("已关闭，可在“设置”中开启。", "Off; turn it on in Settings.")
	}
	return ""
}
