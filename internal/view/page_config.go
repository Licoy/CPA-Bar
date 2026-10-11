package view

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/licoy/cpa-bar/internal/cpa"
)

// The config page edits CPA's v8 configuration: the common settings as a
// form, and the whole document as YAML. The routes, from the CPA v8 source
// (internal/api/server_management_v8.go and handlers/management/config_v8.go):
//
//	GET/PUT/PATCH /config           the document as JSON in the v8 layout
//	GET/PUT/PATCH/DELETE /config/*  a node of it, by YAML key path
//	GET/PUT /config.yaml            the document as YAML
//
// A write is validated by the server (400/422 with {"error","message"}) and
// leaves the file unchanged when invalid. There is no dry-run.

func init() {
	RegisterPage(Page{
		ID:       "config",
		Order:    20,
		Group:    GroupManage,
		Title:    func() string { return T("配置", "Config") },
		Instance: true,
		View:     configPage,
	})
}

// ---- Shared helpers for the configuration pages ----

// warnBanner is a soft warning that tells the page's data changed or that
// something needs a second look; it shows nothing for empty text.
func warnBanner(c *ui.Context, text string) {
	if text == "" {
		return
	}
	t := c.Theme()
	ui.Row(c).Gap(10).Padding(10, 12).Radius(12).Background(t.Warning.Alpha(0.12)).AlignItems(ui.Start).Children(func() {
		ui.Icon(c, icAlert).FontSize(15).TextColor(t.Warning)
		ui.Text(c, text).Grow(1).Basis(0).FontSize(12).Selectable()
	})
}

// cfgMask hides a secret except its first and last four characters.
func cfgMask(s string) string {
	r := []rune(s)
	if len(r) <= 8 {
		return strings.Repeat("•", max(len(r), 4))
	}
	return string(r[:4]) + "…" + string(r[len(r)-4:])
}

// cfgGet reads a node of the configuration; a node that is not there is nil.
func cfgGet(ctx context.Context, cl *cpa.Client, path ...string) (json.RawMessage, error) {
	raw, err := cl.GetConfig(ctx, path...)
	if cpa.KindOf(err) == cpa.KindNotFound {
		return nil, nil
	}
	return raw, err
}

// cfgRoot reads the whole configuration as decoded JSON, numbers kept as
// json.Number.
func cfgRoot(ctx context.Context, cl *cpa.Client) (any, error) {
	raw, err := cl.GetConfig(ctx)
	if err != nil {
		return nil, err
	}
	return cfgDecode(raw)
}

func cfgDecode(raw []byte) (any, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, fmt.Errorf("%s", T("无法解析服务端返回的配置", "Could not parse the configuration the server returned"))
	}
	return v, nil
}

// cfgDig walks decoded JSON along keys.
func cfgDig(root any, path ...string) (any, bool) {
	for _, k := range path {
		m, ok := root.(map[string]any)
		if !ok {
			return nil, false
		}
		if root, ok = m[k]; !ok {
			return nil, false
		}
	}
	return root, true
}

// cfgCanon is a canonical text of a JSON value for comparing what was
// loaded with what the server has now. The server adds auth indexes to
// API-key groups on reads; they are not content.
func cfgCanon(raw []byte) string {
	v, err := cfgDecode(raw)
	if err != nil || v == nil {
		return ""
	}
	v = stripAuthIndex(v)
	b, _ := json.Marshal(v)
	return string(b)
}

func stripAuthIndex(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			if k == "auth_index" || k == "auth-index" {
				continue
			}
			out[k] = stripAuthIndex(e)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = stripAuthIndex(e)
		}
		return out
	}
	return v
}

// cfgChanged is returned by a save that found the server's copy different
// from the one the user started from; nothing was written.
type cfgChanged struct{ detail string }

func (e *cfgChanged) Error() string {
	return T("服务器上的配置在你编辑期间已被修改，未保存。已载入最新内容，请核对后再保存。", "The configuration changed on the server while you were editing; nothing was saved. The latest content is loaded; review it and save again.") + e.detail
}

// cfgJSONBlock shows a subtree read-only, with a way to edit it as YAML.
func cfgJSONBlock(c *ui.Context, env *PageEnv, title string, raw json.RawMessage) {
	t := c.Theme()
	Panel(c).Key("json-" + title).Children(func() {
		PanelTitle(c, icFileCog, TintGray, title, T("只读", "Read-only"), false)
		if len(raw) == 0 || string(bytes.TrimSpace(raw)) == "null" {
			ui.Text(c, T("未配置", "Not configured")).FontSize(12).TextColor(t.TextMuted)
		} else {
			var buf bytes.Buffer
			text := string(raw)
			if json.Indent(&buf, raw, "", "  ") == nil {
				text = buf.String()
			}
			lines := strings.Split(text, "\n")
			if len(lines) > 80 {
				text = strings.Join(lines[:80], "\n") + "\n…"
			}
			ui.Column(c).Padding(10, 12).Radius(12).Background(pal(t).Inset).Children(func() {
				ui.Text(c, text).Font("monospace").FontSize(11).Selectable()
			})
		}
		ui.Row(c).Children(func() {
			if PillButton(c.Key("yaml-"+title), icFileCog, T("在 YAML 中编辑", "Edit as YAML"), false).Clicked() {
				cfgOpenYAML(env)
			}
		})
	})
}

// cfgOpenYAML switches to the config page in YAML mode.
func cfgOpenYAML(env *PageEnv) {
	configStateFor(env).mode = cfgModeYAML
	env.App.page = "config"
}

// ---- Settings form ----

type cfgKind int

const (
	cfgBool cfgKind = iota
	cfgText
	cfgInt
	cfgEnum
)

// cfgField is one setting of the form, at a v8 path of the document.
type cfgField struct {
	path    []string
	kind    cfgKind
	zh, en  string
	helpZh  string
	helpEn  string
	def     string // what the server uses when the key is absent, as text
	options []string
	// optionsZh and optionsEn, when set, name the options in the capsule
	// control that chooses among a few of them.
	optionsZh, optionsEn []string
	// risky settings ask for an extra warning in the confirmation.
	riskZh, riskEn string
}

func (f *cfgField) id() string    { return strings.Join(f.path, "/") }
func (f *cfgField) label() string { return T(f.zh, f.en) }

// cfgVal is a field with the text the server had and the text being edited.
type cfgVal struct {
	f    *cfgField
	base string
	text string
	on   bool
}

func (v *cfgVal) cur() string {
	if v.f.kind == cfgBool {
		return strconv.FormatBool(v.on)
	}
	return strings.TrimSpace(v.text)
}

func (v *cfgVal) dirty() bool { return v.cur() != v.base }

func (v *cfgVal) set(s string) {
	v.text = s
	v.on = s == "true"
}

// invalid explains why the edited value cannot be saved.
func (v *cfgVal) invalid() string {
	if !v.dirty() {
		return ""
	}
	if v.f.kind == cfgInt {
		if _, err := strconv.Atoi(v.cur()); err != nil {
			return T("请输入整数", "Enter a whole number")
		}
	}
	return ""
}

// cfgForm is a set of settings with its load and save.
type cfgForm struct {
	vals     []*cfgVal
	started  bool
	loaded   bool
	load     Task
	save     Task
	conflict string
	confirm  bool
	// groups: field index ranges are in order; a group title starts a section.
}

func newCfgForm(fields []*cfgField) *cfgForm {
	f := &cfgForm{}
	for _, fd := range fields {
		f.vals = append(f.vals, &cfgVal{f: fd})
	}
	return f
}

func (f *cfgForm) any(pred func(*cfgVal) bool) bool {
	for _, v := range f.vals {
		if pred(v) {
			return true
		}
	}
	return false
}

func (f *cfgForm) dirty() bool   { return f.any((*cfgVal).dirty) }
func (f *cfgForm) invalid() bool { return f.any(func(v *cfgVal) bool { return v.invalid() != "" }) }
func (f *cfgForm) discard()      { f.each(func(v *cfgVal) { v.set(v.base) }) }
func (f *cfgForm) each(fn func(*cfgVal)) {
	for _, v := range f.vals {
		fn(v)
	}
}

// applyTexts takes the server's values as the baseline; edited fields keep
// their edits when keepEdits.
func (f *cfgForm) applyTexts(got map[string]string, keepEdits bool) {
	for _, v := range f.vals {
		wasDirty := v.dirty()
		v.base = got[v.f.id()]
		if !keepEdits || !wasDirty {
			v.set(v.base)
		}
	}
}

// cfgText is the text of a field in the decoded document.
func cfgFieldText(root any, f *cfgField) string {
	v, ok := cfgDig(root, f.path...)
	if !ok || v == nil {
		return f.def
	}
	switch x := v.(type) {
	case bool:
		return strconv.FormatBool(x)
	case string:
		return x
	case json.Number:
		return x.String()
	}
	b, _ := json.Marshal(v)
	return string(b)
}

func (f *cfgForm) fields() []*cfgField {
	out := make([]*cfgField, len(f.vals))
	for i, v := range f.vals {
		out[i] = v.f
	}
	return out
}

func cfgFetchTexts(ctx context.Context, cl *cpa.Client, fields []*cfgField) (map[string]string, error) {
	root, err := cfgRoot(ctx, cl)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(fields))
	for _, fd := range fields {
		out[fd.id()] = cfgFieldText(root, fd)
	}
	return out, nil
}

// reload reads the settings from the server.
func (f *cfgForm) reload(env *PageEnv, keepEdits bool) {
	if env.Client == nil {
		return
	}
	f.started = true
	cl, fields := env.Client, f.fields()
	var got map[string]string
	env.Run(&f.load, func(ctx context.Context) (err error) {
		got, err = cfgFetchTexts(ctx, cl, fields)
		return err
	}, func(err error) {
		if err == nil {
			f.applyTexts(got, keepEdits)
			f.loaded = true
		}
	})
}

// patchBody nests the values at their paths: {"routing":{"retry":{...}}}.
func patchBody(vals []*cfgVal) (json.RawMessage, error) {
	root := map[string]any{}
	for _, v := range vals {
		var value any
		switch v.f.kind {
		case cfgBool:
			value = v.on
		case cfgInt:
			n, err := strconv.Atoi(v.cur())
			if err != nil {
				return nil, fmt.Errorf("%s: %s", v.f.label(), T("请输入整数", "Enter a whole number"))
			}
			value = n
		default:
			value = v.cur()
		}
		m := root
		for _, k := range v.f.path[:len(v.f.path)-1] {
			next, _ := m[k].(map[string]any)
			if next == nil {
				next = map[string]any{}
				m[k] = next
			}
			m = next
		}
		m[v.f.path[len(v.f.path)-1]] = value
	}
	return json.Marshal(root)
}

// save writes the changed settings as one patch, unless the server's values
// of those settings are no longer what the form started from.
func (f *cfgForm) saveChanges(env *PageEnv) {
	if env.Client == nil {
		return
	}
	cl := env.Client
	var changed []*cfgVal
	base := map[string]string{}
	for _, v := range f.vals {
		if v.dirty() {
			changed = append(changed, &cfgVal{f: v.f, base: v.base, text: v.text, on: v.on})
			base[v.f.id()] = v.base
		}
	}
	if len(changed) == 0 {
		return
	}
	fields := f.fields()
	var latest map[string]string
	env.Run(&f.save, func(ctx context.Context) error {
		got, err := cfgFetchTexts(ctx, cl, fields)
		if err != nil {
			return err
		}
		latest = got
		var diffs []string
		for _, v := range changed {
			if got[v.f.id()] != base[v.f.id()] {
				diffs = append(diffs, fmt.Sprintf("%s: %s → %s", v.f.label(), cfgShow(base[v.f.id()]), cfgShow(got[v.f.id()])))
			}
		}
		if len(diffs) > 0 {
			return &cfgChanged{detail: "\n" + strings.Join(diffs, "\n")}
		}
		body, err := patchBody(changed)
		if err != nil {
			return err
		}
		return cl.PatchConfig(ctx, body)
	}, func(err error) {
		var ch *cfgChanged
		switch {
		case errors.As(err, &ch):
			f.conflict = ch.Error()
			f.save.Err = nil
			f.applyTexts(latest, true)
		case err == nil:
			f.conflict = ""
			f.reload(env, false)
		case IsIndeterminate(err):
			f.reload(env, true)
		}
	})
}

func cfgShow(s string) string {
	if s == "" {
		return T("(未设置)", "(unset)")
	}
	return s
}

// summary lists the changes for a confirmation.
func (f *cfgForm) summary() string {
	var sb strings.Builder
	for _, v := range f.vals {
		if !v.dirty() {
			continue
		}
		fmt.Fprintf(&sb, "%s: %s → %s\n", v.f.label(), cfgShow(v.base), cfgShow(v.cur()))
		if v.f.riskZh != "" {
			sb.WriteString("  ! " + T(v.f.riskZh, v.f.riskEn) + "\n")
		}
	}
	return strings.TrimRight(sb.String(), "\n")
}

type cfgSection struct {
	zh, en   string
	from, to int // field index range: from, up to but not including to
	icon     *ui.SVG
	tint     Tint
}

// view draws the settings in panels, one per section, with the save controls.
func (f *cfgForm) view(c *ui.Context, env *PageEnv, sections []cfgSection) {
	t := c.Theme()
	if f.load.Running && !f.loaded {
		Busy(c, T("正在读取配置…", "Reading configuration…"))
	}
	if !f.load.Running {
		ErrorBanner(c, f.load.Err)
	}
	if !f.loaded {
		return
	}
	warnBanner(c, f.conflict)
	for _, s := range sections {
		Panel(c).Key("sec-" + s.en).Children(func() {
			modified := 0
			for i := s.from; i < s.to; i++ {
				if f.vals[i].dirty() {
					modified++
				}
			}
			trailing := ""
			if modified > 0 {
				trailing = fmt.Sprintf(T("%d 项已修改", "%d modified"), modified)
			}
			PanelTitle(c, s.icon, s.tint, T(s.zh, s.en), trailing, false)
			ui.Form(c, func() {
				for i := s.from; i < s.to; i++ {
					f.field(c, f.vals[i])
				}
			})
		})
	}

	dirty := f.dirty()
	Card(c).Padding(12, 16).Children(func() {
		ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
			ui.Row(c).Grow(1).Gap(10).AlignItems(ui.Center).Children(func() {
				if dirty {
					n := 0
					f.each(func(v *cfgVal) {
						if v.dirty() {
							n++
						}
					})
					Pill(c, fmt.Sprintf(T("%d 项待保存", "%d unsaved"), n), t.Warning)
				}
				ui.Text(c, T("目标实例：", "Target: ")+env.InstanceName()).FontSize(12).TextColor(t.TextMuted).SingleLine()
			})
			if PillButton(c, cfgIcUndo, T("放弃修改", "Discard"), !dirty || f.save.Running).Clicked() {
				f.discard()
				f.conflict = ""
			}
			if ui.PrimaryButton(c, T("保存更改", "Save Changes")).Disabled(!dirty || f.invalid() || f.save.Running || f.load.Running).
				Tooltip(T("写入到实例：", "Writes to instance: ") + env.InstanceName()).Clicked() {
				f.confirm = true
			}
		})
	})
	TaskStatus(c, &f.save, T("正在保存…", "Saving…"), T("已保存", "Saved"))

	if f.confirm {
		msg := fmt.Sprintf(T("将向实例“%s”写入以下改动：\n%s", "These changes will be written to instance “%s”:\n%s"), env.InstanceName(), f.summary())
		if ui.AlertDialog(c, &f.confirm, T("保存配置？", "Save configuration?"), msg, T("取消", "Cancel"), T("确认保存", "Confirm Save")) == 1 {
			f.saveChanges(env)
		}
	}
}

// field draws one setting: its control, a marker when modified, and the
// description and the problem under it.
func (f *cfgForm) field(c *ui.Context, v *cfgVal) {
	id := v.f.id()
	desc := T(v.f.helpZh, v.f.helpEn)
	if v.dirty() {
		desc = T("已修改 · ", "Modified · ") + desc
	}
	fld := ui.Field(c.Key("fld-"+id), v.f.label(), func() {
		switch v.f.kind {
		case cfgBool:
			ui.Switch(c.Key(id), &v.on)
		case cfgEnum:
			if len(v.f.options) <= 4 {
				cfgEnumTabs(c, v)
			} else {
				ui.Select(c.Key(id), &v.text, v.f.options).Width(220)
			}
		default:
			ui.TextInput(c.Key(id), &v.text).Width(260).Placeholder(cfgShow(v.f.def))
		}
	})
	if desc != "" {
		fld.Description(desc)
	}
	if msg := v.invalid(); msg != "" {
		fld.Error(msg)
	}
}

// cfgEnumTabs chooses among the few values of an enum with a capsule control.
func cfgEnumTabs(c *ui.Context, v *cfgVal) {
	labels := make([]string, len(v.f.options))
	for i, o := range v.f.options {
		labels[i] = o
		if i < len(v.f.optionsZh) {
			labels[i] = T(v.f.optionsZh[i], v.f.optionsEn[i])
		}
	}
	idx := slices.Index(v.f.options, v.text)
	if idx < 0 {
		idx = max(0, slices.Index(v.f.options, v.f.def))
	}
	if PillTabs(c.Key(v.f.id()), &idx, labels...) {
		v.text = v.f.options[idx]
	}
}

// ---- The page ----

const (
	cfgModeForm = iota
	cfgModeYAML
)

func configFields() []*cfgField {
	return []*cfgField{
		{path: []string{"observability", "logs", "debug"}, kind: cfgBool, zh: "调试日志", en: "Debug logging", helpZh: "observability.logs.debug", helpEn: "observability.logs.debug", def: "false"},
		{path: []string{"observability", "logs", "logging-to-file"}, kind: cfgBool, zh: "日志写入文件", en: "Log to file", helpZh: "observability.logs.logging-to-file", helpEn: "observability.logs.logging-to-file", def: "false"},
		{path: []string{"observability", "logs", "request-log"}, kind: cfgBool, zh: "记录请求日志", en: "Request logging", helpZh: "observability.logs.request-log；日志可能含请求内容", helpEn: "observability.logs.request-log; logs may contain request content", def: "false"},
		{path: []string{"observability", "usage", "usage-statistics-enabled"}, kind: cfgBool, zh: "用量统计", en: "Usage statistics", helpZh: "observability.usage.usage-statistics-enabled", helpEn: "observability.usage.usage-statistics-enabled", def: "false"},
		{path: []string{"requests", "proxy-url"}, kind: cfgText, zh: "全局代理", en: "Global proxy", helpZh: "requests.proxy-url；支持 socks5/http/https，留空为不使用", helpEn: "requests.proxy-url; socks5/http/https, empty for none"},
		{path: []string{"requests", "passthrough-headers"}, kind: cfgBool, zh: "透传上游响应头", en: "Pass upstream headers through", helpZh: "requests.passthrough-headers", helpEn: "requests.passthrough-headers", def: "false"},
		{path: []string{"requests", "nonstream-keepalive-interval"}, kind: cfgInt, zh: "非流式保活间隔（秒）", en: "Non-stream keep-alive (s)", helpZh: "requests.nonstream-keepalive-interval；大于 0 时按此间隔发送空行", helpEn: "requests.nonstream-keepalive-interval; blank lines every N seconds when above 0"},
		{path: []string{"routing", "retry", "request-retry"}, kind: cfgInt, zh: "请求重试轮数", en: "Request retry rounds", helpZh: "routing.retry.request-retry", helpEn: "routing.retry.request-retry"},
		{path: []string{"routing", "retry", "max-retry-credentials"}, kind: cfgInt, zh: "每轮最多尝试的账号数", en: "Max credentials per round", helpZh: "routing.retry.max-retry-credentials；0 为全部", helpEn: "routing.retry.max-retry-credentials; 0 tries all"},
		{path: []string{"routing", "retry", "max-retry-interval"}, kind: cfgInt, zh: "重试最长等待（秒）", en: "Max retry wait (s)", helpZh: "routing.retry.max-retry-interval", helpEn: "routing.retry.max-retry-interval"},
		{path: []string{"routing", "cooldown", "disable-cooling"}, kind: cfgBool, zh: "禁用冷却", en: "Disable cooling", helpZh: "routing.cooldown.disable-cooling；失败后不再让账号冷却", helpEn: "routing.cooldown.disable-cooling; failed credentials are not cooled down", def: "false"},
		{path: []string{"management", "allow-remote"}, kind: cfgBool, zh: "允许远程管理", en: "Allow remote management", helpZh: "management.allow-remote；关闭后只有服务器本机能访问管理接口", helpEn: "management.allow-remote; when off only the server itself can reach the management API", def: "false",
			riskZh: "关闭后，从其它机器访问管理接口（包括 CPA-Bar）将被拒绝；打开后管理接口对外网可达。", riskEn: "Turning it off blocks management access from other machines, including CPA-Bar; turning it on exposes the management API to the network."},
	}
}

func configSections() []cfgSection {
	return []cfgSection{
		{zh: "日志与统计", en: "Logs & Statistics", from: 0, to: 4, icon: icLogs, tint: TintBlue},
		{zh: "网络与请求", en: "Network & Requests", from: 4, to: 7, icon: icCloud, tint: TintTeal},
		{zh: "重试与冷却", en: "Retry & Cooldown", from: 7, to: 11, icon: icRefresh, tint: TintOrange},
		{zh: "管理接口", en: "Management API", from: 11, to: 12, icon: cfgIcShield, tint: TintPurple},
	}
}

type configState struct {
	mode int
	form *cfgForm
	yaml *yamlEditor
}

func newConfigState() *configState {
	return &configState{form: newCfgForm(configFields()), yaml: newYAMLEditor()}
}

func configStateFor(env *PageEnv) *configState { return State(env, "config", newConfigState) }

func configPage(c *ui.Context, env *PageEnv) {
	t := c.Theme()
	st := configStateFor(env)
	PageHeader(c, T("配置", "Config"), T("实例配置文件（v8 布局）。写入前会核对服务器上的内容是否已变化。", "The instance's configuration file (v8 layout). Before a write the server's content is checked for changes."), func() {
		busy := st.form.load.Running || st.yaml.load.Running
		if BusyPillButton(c, icRefresh, T("重新加载", "Reload"), busy, env.Client == nil).Clicked() {
			if st.mode == cfgModeYAML {
				st.yaml.askReload(env)
			} else {
				st.form.reload(env, false)
			}
		}
	})
	if env.Client == nil {
		return
	}
	mode := st.mode
	ui.Row(c).Children(func() {
		if PillTabs(c.Key("mode"), &mode, T("表单", "Form"), "YAML") {
			st.mode = mode
		}
	})
	if st.mode == cfgModeYAML {
		if !st.yaml.started {
			st.yaml.reload(env, true)
		}
		st.yaml.view(c, env)
		return
	}
	if !st.form.started {
		st.form.reload(env, false)
	}
	st.form.view(c, env, configSections())
	Panel(c).Children(func() {
		PanelTitle(c, icFileCog, TintGray, T("其它设置", "Other settings"), "", false)
		ui.Text(c, T("监听地址、TLS、插件、payload 规则等更多设置请在 YAML 模式中编辑。", "Listener, TLS, plugins, payload rules and other settings are edited in YAML mode.")).FontSize(12).TextColor(t.TextMuted)
		ui.Row(c).Children(func() {
			if PillButton(c, icFileCog, T("切换到 YAML", "Switch to YAML"), false).Clicked() {
				st.mode = cfgModeYAML
			}
		})
	})
}

// ---- YAML editor ----

type yamlEditor struct {
	started bool
	loaded  bool
	base    string // the server's text as last read
	baseGen int
	edit    *ui.TextBuffer

	text     string // the editor's text, as of version ver
	ver      uint64
	synced   int // baseGen at the last sync; -1 before the first
	dirty    bool
	preview  bool
	conflict bool

	diff      []DiffLine
	diffVer   uint64
	diffGen   int
	diffValid bool

	load, save                                Task
	confirmSave, confirmReload, confirmRevert bool
}

func newYAMLEditor() *yamlEditor {
	return &yamlEditor{edit: ui.NewTextBuffer(""), synced: -1}
}

// sync keeps the cached text and dirty flag current without reading the
// whole editor on every frame.
func (y *yamlEditor) sync() {
	if v := y.edit.Version(); y.synced != y.baseGen || v != y.ver {
		y.ver, y.synced = v, y.baseGen
		y.text = y.edit.String()
		y.dirty = y.text != y.base
	}
}

func (y *yamlEditor) setBase(s string) {
	y.base = s
	y.baseGen++
}

func (y *yamlEditor) revert() {
	y.edit.Set(y.base)
	y.conflict = false
	y.preview = false
}

// reload reads the server's YAML; replace also puts it in the editor.
func (y *yamlEditor) reload(env *PageEnv, replace bool) {
	if env.Client == nil {
		return
	}
	y.started = true
	cl := env.Client
	var text string
	env.Run(&y.load, func(ctx context.Context) (err error) {
		text, err = cl.GetConfigYAML(ctx)
		return err
	}, func(err error) {
		if err != nil {
			return
		}
		y.setBase(text)
		if replace || !y.loaded {
			y.edit.Set(text)
			y.conflict = false
		}
		y.loaded = true
	})
}

// askReload reloads, after confirming when that would drop edits.
func (y *yamlEditor) askReload(env *PageEnv) {
	y.sync()
	if y.dirty {
		y.confirmReload = true
		return
	}
	y.reload(env, true)
}

func (y *yamlEditor) diffLines() []DiffLine {
	v := y.edit.Version()
	if !y.diffValid || y.diffVer != v || y.diffGen != y.baseGen {
		y.diff = DiffLines(y.base, y.text)
		y.diffVer, y.diffGen, y.diffValid = v, y.baseGen, true
	}
	return y.diff
}

var errYAMLChanged = errors.New("the server's YAML changed")

func (y *yamlEditor) startSave(env *PageEnv) {
	cl, doc, base := env.Client, y.text, y.base
	var remote string
	env.Run(&y.save, func(ctx context.Context) error {
		cur, err := cl.GetConfigYAML(ctx)
		if err != nil {
			return err
		}
		if cur != base {
			remote = cur
			return errYAMLChanged
		}
		return cl.PutConfigYAML(ctx, doc)
	}, func(err error) {
		switch {
		case errors.Is(err, errYAMLChanged):
			// Keep the edit; show it against what the server has now.
			y.setBase(remote)
			y.conflict, y.preview = true, true
			y.save.Err = nil
		case err == nil:
			y.conflict = false
			y.reload(env, true)
		case IsIndeterminate(err):
			y.reload(env, false)
		}
	})
}

func (y *yamlEditor) view(c *ui.Context, env *PageEnv) {
	t := c.Theme()
	if y.load.Running && !y.loaded {
		Busy(c, T("正在读取 YAML…", "Reading YAML…"))
	}
	if !y.load.Running {
		ErrorBanner(c, y.load.Err)
	}
	if !y.loaded {
		return
	}
	y.sync()
	Notice(c, T("YAML 含有上游密钥等明文敏感信息（管理密钥在服务器上是哈希保存的）。CPA-Bar 不会自动复制或记录其内容。", "The YAML holds plain secrets such as upstream API keys (the management key is stored hashed on the server). CPA-Bar never copies or logs its content automatically."))
	if y.conflict {
		warnBanner(c, new(cfgChanged).Error())
	}

	added, removed := DiffStat(y.diffLines())
	Panel(c).Children(func() {
		trailing := T("与服务器一致", "Same as the server")
		if y.dirty {
			trailing = fmt.Sprintf("+%d / −%d", added, removed)
		}
		PanelTitle(c, icFileCog, TintBlue, "YAML", trailing, false)
		ui.Column(c).Padding(6).Radius(12).Background(pal(t).Inset).Children(func() {
			ui.TextAreaBuffer(c.Key("yaml"), y.edit).Height(440).Font("monospace").FontSize(12).Label("YAML")
		})
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			busy := y.save.Running || y.load.Running
			ui.Text(c, T("目标实例：", "Target: ")+env.InstanceName()).FontSize(12).TextColor(t.TextMuted).SingleLine().Grow(1)
			if PillButton(c, cfgIcUndo, T("放弃修改", "Discard Changes"), !y.dirty || busy).Clicked() {
				y.confirmRevert = true
			}
			label := T("预览改动", "Preview Changes")
			if y.preview {
				label = T("收起预览", "Hide Preview")
			}
			if PillButton(c, cfgIcDiff, label, !y.dirty && !y.preview).Clicked() {
				y.preview = !y.preview
			}
			if ui.PrimaryButton(c, T("保存", "Save")).Disabled(!y.dirty || busy).
				Tooltip(T("写入到实例：", "Writes to instance: ") + env.InstanceName()).Clicked() {
				y.confirmSave = true
			}
		})
	})
	TaskStatus(c, &y.save, T("正在保存…", "Saving…"), T("已保存", "Saved"))

	if y.preview {
		Panel(c).Children(func() {
			PanelTitle(c, cfgIcDiff, TintOrange, T("改动预览", "Changes"), T("服务器 → 编辑", "server → edit"), false)
			diffView(c, y.diffLines())
		})
	}

	if y.confirmSave {
		msg := fmt.Sprintf(T("将用编辑后的内容整体替换实例“%s”的配置文件（+%d / −%d 行）。服务器会校验内容，无效时不会写入。", "The configuration file of instance “%s” will be replaced as a whole by your edit (+%d / −%d lines). The server validates it and writes nothing if it is invalid."), env.InstanceName(), added, removed)
		if ui.AlertDialog(c, &y.confirmSave, T("替换整个配置？", "Replace the whole configuration?"), msg, T("取消", "Cancel"), T("确认保存", "Confirm Save")) == 1 {
			y.startSave(env)
		}
	}
	if y.confirmReload {
		if ui.AlertDialog(c, &y.confirmReload, T("丢弃未保存的修改？", "Discard unsaved edits?"), T("重新加载会用服务器上的内容覆盖编辑器。", "Reloading replaces the editor with the server's content."), T("取消", "Cancel"), T("确认重新加载", "Confirm Reload")) == 1 {
			y.reload(env, true)
		}
	}
	if y.confirmRevert {
		if ui.AlertDialog(c, &y.confirmRevert, T("放弃所有修改？", "Discard all edits?"), T("编辑器将恢复为服务器上的内容。", "The editor goes back to the server's content."), T("取消", "Cancel"), T("确认放弃", "Confirm Discard")) == 1 {
			y.revert()
		}
	}
}

// diffViewLimit caps the rows a diff view draws.
const diffViewLimit = 400

// diffView draws a diff: removed lines in the danger color, added lines in
// the success color, unchanged runs folded away.
func diffView(c *ui.Context, lines []DiffLine) {
	t := c.Theme()
	rows := CollapseDiff(lines, 3)
	ui.Column(c).Gap(0).Padding(6, 0).Radius(12).Background(pal(t).Inset).Children(func() {
		if added, removed := DiffStat(lines); added+removed == 0 {
			ui.Text(c, T("没有改动", "No changes")).FontSize(12).TextColor(t.TextMuted).Padding(4, 12)
			return
		}
		for i, r := range rows {
			if i == diffViewLimit {
				ui.Text(c, fmt.Sprintf(T("… 还有 %d 行未显示", "… %d more rows not shown"), len(rows)-i)).FontSize(11).TextColor(t.TextMuted).Padding(4, 12)
				break
			}
			if r.Skipped > 0 {
				ui.Text(c, fmt.Sprintf(T("⋯ %d 行未改动", "⋯ %d unchanged lines"), r.Skipped)).Key(i).FontSize(11).TextColor(t.TextMuted).Padding(2, 12)
				continue
			}
			l := r.Line
			col, mark := t.TextMuted, " "
			bg := ui.Color{}
			switch l.Op {
			case DiffInsert:
				col, mark, bg = t.Success, "+", t.Success.Alpha(0.10)
			case DiffDelete:
				col, mark, bg = t.Danger, "−", t.Danger.Alpha(0.10)
			}
			ui.Row(c).Key(i).Gap(6).Padding(0, 12).Background(bg).Children(func() {
				num := func(n int) string {
					if n == 0 {
						return ""
					}
					return strconv.Itoa(n)
				}
				ui.Text(c, num(l.Old)).Width(38).FontSize(11).Font("monospace").TextColor(t.TextMuted).TextAlign(ui.End)
				ui.Text(c, num(l.New)).Width(38).FontSize(11).Font("monospace").TextColor(t.TextMuted).TextAlign(ui.End)
				ui.Text(c, mark).Width(10).FontSize(12).Font("monospace").TextColor(col)
				ui.Text(c, l.Text).Grow(1).FontSize(12).Font("monospace").TextColor(pick(l.Op == DiffEqual, t.Text, col)).Selectable()
			})
		}
	})
}

// cfgDangerPill is a capsule button for a destructive action: the same as
// PillButton, in the danger color.
func cfgDangerPill(c *ui.Context, icon *ui.SVG, label string, disabled bool) ui.Element {
	t := c.Theme()
	b := ui.ButtonBase(c).Label(label).Disabled(disabled).Padding(6, 12).Gap(6).Radius(999).AlignItems(ui.Center)
	bg := t.Danger.Alpha(0.10)
	switch {
	case disabled:
		bg = t.Text.Alpha(0.06)
	case b.Pressed():
		bg = t.Danger.Alpha(0.24)
	case b.Hovered():
		bg = t.Danger.Alpha(0.17)
	}
	b.Background(bg)
	return b.Children(func() {
		col := t.Danger
		if disabled {
			col = t.TextMuted
		}
		if icon != nil {
			ui.Icon(c, icon).FontSize(13).TextColor(col)
		}
		ui.Text(c, label).FontSize(13).FontWeight(500).TextColor(col).SingleLine()
	})
}
