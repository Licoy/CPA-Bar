package view

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"
)

// Upstream providers are the v8 configuration's top-level api-keys: a map
// from provider to a list of groups. A group has one endpoint (base-url),
// settings its keys share, and a keys list; each key may override some
// settings (config.example.yaml, internal/config/config_v8.go). The list of
// one provider is read and written as a whole at /config/api-keys/<provider>
// (a write replaces the list). Fields the form does not edit travel through
// untouched, because every group is kept as raw JSON.

func init() {
	RegisterPage(Page{
		ID:       "providers",
		Order:    10,
		Group:    GroupManage,
		Title:    func() string { return T("上游提供商", "Upstream Providers") },
		Instance: true,
		View:     providersPage,
	})
}

type provKind struct {
	id     string
	zh, en string
}

// providerKinds are the provider families the server accepts under api-keys
// (v8KeyFamilies in internal/config/config_v8.go).
var providerKinds = []provKind{
	{"gemini", "Gemini", "Gemini"},
	{"interactions", "Gemini Interactions", "Gemini Interactions"},
	{"vertex", "Vertex 兼容", "Vertex-compatible"},
	{"codex", "Codex", "Codex"},
	{"claude", "Claude", "Claude"},
	{"xai", "xAI", "xAI"},
	{"meta", "Meta", "Meta"},
	{"openai-compatibility", "OpenAI 兼容", "OpenAI-compatible"},
}

const provOpenAI = "openai-compatibility"

func (k provKind) title() string { return T(k.zh, k.en) }

// ---- Raw JSON objects ----

type rawObj = map[string]json.RawMessage

func rawIsNull(v json.RawMessage) bool {
	s := strings.TrimSpace(string(v))
	return s == "" || s == "null"
}

func rawString(m rawObj, k string) string {
	var s string
	if json.Unmarshal(m[k], &s) == nil {
		return s
	}
	return ""
}

func rawNumber(m rawObj, k string) string {
	if rawIsNull(m[k]) {
		return ""
	}
	var n json.Number
	if json.Unmarshal(m[k], &n) == nil {
		return n.String()
	}
	return ""
}

func rawBool(m rawObj, k string) bool {
	var b bool
	return json.Unmarshal(m[k], &b) == nil && b
}

func rawObjs(m rawObj, k string) []rawObj {
	var l []rawObj
	if json.Unmarshal(m[k], &l) != nil {
		return nil
	}
	return l
}

func rawStrings(m rawObj, k string) []string {
	var l []string
	if json.Unmarshal(m[k], &l) != nil {
		return nil
	}
	return l
}

func rawHeaders(m rawObj, k string) map[string]string {
	var h map[string]string
	if json.Unmarshal(m[k], &h) != nil {
		return nil
	}
	return h
}

func jsonRaw(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

func cloneRaw(m rawObj) rawObj {
	out := make(rawObj, len(m)+4)
	for k, v := range m {
		out[k] = v
	}
	return out
}

func dropAuthIndex(m rawObj) {
	delete(m, "auth_index")
	delete(m, "auth-index")
}

// setStr stores text at key, but only when the user changed it from what was
// loaded; an empty text removes the key.
func setStr(m rawObj, key, cur, orig string) {
	if cur == orig {
		return
	}
	if cur == "" {
		delete(m, key)
		return
	}
	m[key] = jsonRaw(cur)
}

func setInt(m rawObj, key, cur, orig string) error {
	cur = strings.TrimSpace(cur)
	if cur == orig {
		return nil
	}
	if cur == "" {
		delete(m, key)
		return nil
	}
	n, err := strconv.Atoi(cur)
	if err != nil {
		return fmt.Errorf("%s: %s", key, T("请输入整数", "Enter a whole number"))
	}
	m[key] = jsonRaw(n)
	return nil
}

func linesOf(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}

func headersText(h map[string]string) string {
	keys := make([]string, 0, len(h))
	for k := range h {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	lines := make([]string, len(keys))
	for i, k := range keys {
		lines[i] = k + ": " + h[k]
	}
	return strings.Join(lines, "\n")
}

func parseHeaders(s string) (map[string]string, error) {
	out := map[string]string{}
	for _, l := range linesOf(s) {
		k, v, ok := strings.Cut(l, ":")
		if !ok || strings.TrimSpace(k) == "" {
			return nil, fmt.Errorf("%s: %s", T("请求头格式应为 名称: 值", "A header must be written Name: value"), l)
		}
		out[strings.TrimSpace(k)] = strings.TrimSpace(v)
	}
	return out, nil
}

// ---- Page state ----

type providersState struct {
	started bool
	loaded  bool
	load    Task
	op      Task
	// groups and canon are what the server had, per provider.
	groups map[string][]rawObj
	canon  map[string]string

	conflict string
	// cf is the write that found the server's list changed, with what changed:
	// nothing was written, and the edit waits for the user's decision.
	cf           *provConflict
	confirmRetry bool
	reveal       map[string]bool
	editor       *provEditor
	delete       struct {
		provider string
		index    int
		open     bool
	}
}

// provConflict is an edit that was not written because the server's list of
// the provider differs from the one it was made on. diff shows the loaded
// list against the server's now (secrets masked); apply makes the edit on a
// fresh copy of the server's list.
type provConflict struct {
	provider string
	desc     string
	diff     []DiffLine
	apply    func(fresh []rawObj) ([]rawObj, error)
	onOK     func()
}

func newProvidersState() *providersState {
	return &providersState{groups: map[string][]rawObj{}, canon: map[string]string{}, reveal: map[string]bool{}}
}

func parseGroups(raw json.RawMessage) ([]rawObj, error) {
	if rawIsNull(raw) {
		return nil, nil
	}
	var l []rawObj
	if err := json.Unmarshal(raw, &l); err != nil {
		return nil, fmt.Errorf("%s", T("无法解析提供商列表", "Could not parse the provider list"))
	}
	for _, g := range l {
		dropAuthIndex(g)
		for _, k := range rawObjs(g, "keys") {
			dropAuthIndex(k)
		}
	}
	return l, nil
}

// withoutAuthIndex returns the group with the server-added indexes removed,
// including those of its keys.
func withoutAuthIndex(g rawObj) rawObj {
	out := cloneRaw(g)
	dropAuthIndex(out)
	if keys := rawObjs(out, "keys"); keys != nil {
		for _, k := range keys {
			dropAuthIndex(k)
		}
		out["keys"] = jsonRaw(keys)
	}
	return out
}

func (s *providersState) setProvider(id string, raw json.RawMessage) {
	groups, err := parseGroups(raw)
	if err != nil {
		groups = nil
	}
	for i, g := range groups {
		groups[i] = withoutAuthIndex(g)
	}
	s.groups[id] = groups
	s.canon[id] = cfgCanon(raw)
}

func (s *providersState) reload(env *PageEnv) {
	if env.Client == nil {
		return
	}
	s.started = true
	cl := env.Client
	var all map[string]json.RawMessage
	env.Run(&s.load, func(ctx context.Context) error {
		raw, err := cfgGet(ctx, cl, "api-keys")
		if err != nil {
			return err
		}
		all = nil
		if !rawIsNull(raw) {
			if err := json.Unmarshal(raw, &all); err != nil {
				return fmt.Errorf("%s", T("无法解析提供商配置", "Could not parse the provider configuration"))
			}
		}
		return nil
	}, func(err error) {
		if err != nil {
			return
		}
		for _, k := range providerKinds {
			s.setProvider(k.id, all[k.id])
		}
		s.loaded = true
	})
}

// writeList changes one provider's list from a fresh copy, unless the
// server's list is no longer the one shown. The whole list is sent. desc says
// what the edit is, for the report of a conflict, which keeps the edit to be
// applied on the server's list if the user decides so (retryConflict).
func (s *providersState) writeList(env *PageEnv, provider, desc string, edit func(fresh []rawObj) ([]rawObj, error), onOK func()) {
	cl := env.Client
	baseline := s.canon[provider]
	loaded := s.groups[provider]
	var latest json.RawMessage
	env.Run(&s.op, func(ctx context.Context) error {
		raw, err := cfgGet(ctx, cl, "api-keys", provider)
		if err != nil {
			return err
		}
		if cfgCanon(raw) != baseline {
			latest = raw
			return &cfgChanged{}
		}
		fresh, err := parseGroups(raw)
		if err != nil {
			return err
		}
		next, err := edit(fresh)
		if err != nil {
			return err
		}
		if next == nil {
			next = []rawObj{}
		}
		return cl.PutConfig(ctx, jsonRaw(next), "api-keys", provider)
	}, func(err error) {
		var ch *cfgChanged
		switch {
		case errors.As(err, &ch):
			before := provDiffText(loaded)
			s.setProvider(provider, latest)
			s.conflict = ch.Error()
			s.cf = &provConflict{provider: provider, desc: desc, diff: DiffLines(before, provDiffText(s.groups[provider])), apply: edit, onOK: onOK}
			s.op.Err = nil
			s.editor = nil
		case err == nil:
			s.conflict = ""
			if s.cf != nil && s.cf.provider == provider {
				s.cf = nil
			}
			onOK()
			s.reload(env)
		case IsIndeterminate(err):
			s.reload(env)
		}
	})
}

// retryConflict applies the edit that met a conflict on the server's current
// list; the write still checks that the list is the one the user was shown.
func (s *providersState) retryConflict(env *PageEnv) {
	if cf := s.cf; cf != nil {
		s.writeList(env, cf.provider, cf.desc, cf.apply, cf.onOK)
	}
}

// dropConflict forgets the edit that met a conflict and reads the server's
// lists afresh.
func (s *providersState) dropConflict(env *PageEnv) {
	s.cf, s.conflict, s.confirmRetry = nil, "", false
	s.reload(env)
}

// provDiffText is the groups as indented JSON for a diff. Keys and header
// values are masked, with a short fingerprint so that a changed secret still
// shows as a change.
func provDiffText(groups []rawObj) string {
	dec := json.NewDecoder(strings.NewReader(string(jsonRaw(groups))))
	dec.UseNumber()
	var v any
	if dec.Decode(&v) != nil || v == nil {
		return ""
	}
	b, _ := json.MarshalIndent(redactSecrets(v, false), "", "  ")
	return string(b)
}

func redactSecrets(v any, headers bool) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			switch s, isStr := e.(string); {
			case isStr && (k == "api-key" || headers):
				out[k] = secretFingerprint(s)
			default:
				out[k] = redactSecrets(e, k == "headers")
			}
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = redactSecrets(e, false)
		}
		return out
	}
	return v
}

// secretFingerprint masks a secret and adds four characters of its hash.
func secretFingerprint(s string) string {
	sum := sha256.Sum256([]byte(s))
	return fmt.Sprintf("%s #%x", cfgMask(s), sum[:2])
}

// groupCanon is a group's content, for telling groups apart.
func groupCanon(g rawObj) string { return cfgCanon(jsonRaw(withoutAuthIndex(g))) }

// locateGroup finds the group a user edited in a fresh list: the identical
// one, else the one that is the same group by name or endpoint, which the
// server's other writer changed. -1 when it is gone.
func locateGroup(fresh []rawObj, idx int, orig rawObj) int {
	want := groupCanon(orig)
	if idx >= 0 && idx < len(fresh) && groupCanon(fresh[idx]) == want {
		return idx
	}
	for i, g := range fresh {
		if groupCanon(g) == want {
			return i
		}
	}
	title := groupTitle(orig)
	if idx >= 0 && idx < len(fresh) && groupTitle(fresh[idx]) == title {
		return idx
	}
	if title != groupTitle(nil) {
		for i, g := range fresh {
			if groupTitle(g) == title {
				return i
			}
		}
	}
	return -1
}

var errGroupGone = fmt.Errorf("%s", T("该分组已不在服务器上", "The group is no longer on the server"))

// ---- Editor ----

type provKeyRow struct {
	raw                   rawObj
	key, weight, proxy    string
	okey, oweight, oproxy string
	show                  bool
}

type provModelRow struct {
	raw           rawObj
	name, alias   string
	oname, oalias string
}

type provEditor struct {
	provider string
	index    int // -1 when adding
	raw      rawObj

	name, baseURL, prefix, proxy, priority string
	o                                      struct{ name, baseURL, prefix, proxy, priority string }
	disabled, odisabled                    bool
	keys                                   []*provKeyRow
	models                                 []*provModelRow
	excluded, oexcluded                    string
	headers, oheaders                      string
	extras                                 []string
	open                                   bool
	problem                                string
}

// groupFields and the like are the fields the form edits; every other field
// of a group, a key or a model is kept as it is.
var (
	groupEditable = map[string]bool{"name": true, "base-url": true, "prefix": true, "proxy-url": true, "priority": true,
		"disabled": true, "keys": true, "models": true, "excluded-models": true, "headers": true}
	keyEditable   = map[string]bool{"api-key": true, "weight": true, "proxy-url": true}
	modelEditable = map[string]bool{"name": true, "alias": true}
)

func newProvEditor(provider string, index int, g rawObj) *provEditor {
	e := &provEditor{provider: provider, index: index, raw: cloneRaw(g), open: true}
	e.name, e.baseURL, e.prefix = rawString(g, "name"), rawString(g, "base-url"), rawString(g, "prefix")
	e.proxy, e.priority = rawString(g, "proxy-url"), rawNumber(g, "priority")
	e.disabled = rawBool(g, "disabled")
	e.o.name, e.o.baseURL, e.o.prefix, e.o.proxy, e.o.priority = e.name, e.baseURL, e.prefix, e.proxy, e.priority
	e.odisabled = e.disabled
	e.excluded = strings.Join(rawStrings(g, "excluded-models"), "\n")
	e.oexcluded = e.excluded
	e.headers = headersText(rawHeaders(g, "headers"))
	e.oheaders = e.headers

	extra := map[string]bool{}
	for k := range g {
		if !groupEditable[k] {
			extra[k] = true
		}
	}
	for _, k := range rawObjs(g, "keys") {
		row := &provKeyRow{raw: k, key: rawString(k, "api-key"), weight: rawNumber(k, "weight"), proxy: rawString(k, "proxy-url")}
		row.okey, row.oweight, row.oproxy = row.key, row.weight, row.proxy
		e.keys = append(e.keys, row)
		for f := range k {
			if !keyEditable[f] {
				extra[f] = true
			}
		}
	}
	for _, m := range rawObjs(g, "models") {
		row := &provModelRow{raw: m, name: rawString(m, "name"), alias: rawString(m, "alias")}
		row.oname, row.oalias = row.name, row.alias
		e.models = append(e.models, row)
		for f := range m {
			if !modelEditable[f] {
				extra[f] = true
			}
		}
	}
	for k := range extra {
		e.extras = append(e.extras, k)
	}
	sort.Strings(e.extras)
	return e
}

// build makes the group as it will be sent.
func (e *provEditor) build() (rawObj, error) {
	out := cloneRaw(e.raw)
	setStr(out, "name", strings.TrimSpace(e.name), e.o.name)
	setStr(out, "base-url", strings.TrimSpace(e.baseURL), e.o.baseURL)
	setStr(out, "prefix", strings.TrimSpace(e.prefix), e.o.prefix)
	setStr(out, "proxy-url", strings.TrimSpace(e.proxy), e.o.proxy)
	if err := setInt(out, "priority", e.priority, e.o.priority); err != nil {
		return nil, err
	}
	if e.provider == provOpenAI && e.disabled != e.odisabled {
		if e.disabled {
			out["disabled"] = jsonRaw(true)
		} else {
			delete(out, "disabled")
		}
	}
	if e.excluded != e.oexcluded {
		if l := linesOf(e.excluded); len(l) > 0 {
			out["excluded-models"] = jsonRaw(l)
		} else {
			delete(out, "excluded-models")
		}
	}
	if e.headers != e.oheaders {
		h, err := parseHeaders(e.headers)
		if err != nil {
			return nil, err
		}
		if len(h) > 0 {
			out["headers"] = jsonRaw(h)
		} else {
			delete(out, "headers")
		}
	}
	keys := []rawObj{}
	for _, r := range e.keys {
		k := cloneRaw(r.raw)
		dropAuthIndex(k)
		k["api-key"] = jsonRaw(strings.TrimSpace(r.key))
		if err := setInt(k, "weight", r.weight, r.oweight); err != nil {
			return nil, err
		}
		setStr(k, "proxy-url", strings.TrimSpace(r.proxy), r.oproxy)
		keys = append(keys, k)
	}
	out["keys"] = jsonRaw(keys)
	if len(e.models) > 0 || out["models"] != nil {
		models := []rawObj{}
		for _, r := range e.models {
			if strings.TrimSpace(r.name) == "" && strings.TrimSpace(r.alias) == "" {
				continue
			}
			m := cloneRaw(r.raw)
			m["name"] = jsonRaw(strings.TrimSpace(r.name))
			m["alias"] = jsonRaw(strings.TrimSpace(r.alias))
			models = append(models, m)
		}
		if len(models) > 0 {
			out["models"] = jsonRaw(models)
		} else {
			delete(out, "models")
		}
	}
	return out, nil
}

// check says what is missing or wrong, "" when the group can be saved.
func (e *provEditor) check() string {
	if e.provider == provOpenAI {
		if strings.TrimSpace(e.name) == "" {
			return T("请填写名称", "A name is required")
		}
		if strings.TrimSpace(e.baseURL) == "" {
			return T("请填写 base-url", "base-url is required")
		}
	} else if len(e.keys) == 0 {
		return T("至少需要一个密钥", "At least one key is required")
	}
	for _, k := range e.keys {
		if strings.TrimSpace(k.key) == "" && e.provider != provOpenAI {
			return T("密钥不能为空", "A key cannot be empty")
		}
	}
	for _, n := range []string{e.priority} {
		if n = strings.TrimSpace(n); n != "" {
			if _, err := strconv.Atoi(n); err != nil {
				return "priority: " + T("请输入整数", "Enter a whole number")
			}
		}
	}
	for _, k := range e.keys {
		if w := strings.TrimSpace(k.weight); w != "" {
			if _, err := strconv.Atoi(w); err != nil {
				return "weight: " + T("请输入整数", "Enter a whole number")
			}
		}
	}
	if e.headers != e.oheaders {
		if _, err := parseHeaders(e.headers); err != nil {
			return err.Error()
		}
	}
	return ""
}

func (e *provEditor) view(c *ui.Context, env *PageEnv, st *providersState) {
	t := c.Theme()
	kind := providerKindOf(e.provider)
	busy := st.op.Running
	ui.Modal(c, &e.open, func() {
		ui.Column(c).Width(700).Padding(20).Gap(10).Children(func() {
			title := T("编辑分组", "Edit Group")
			if e.index < 0 {
				title = T("添加分组", "Add Group")
			}
			ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
				providerTile(c, provLogo(kind.id), 30)
				ui.Text(c, title+" · "+kind.title()).FontSize(17).FontWeight(600)
			})
			ui.Scroll(c.Key("prov-editor")).MaxHeight(dialogHeight(c, 520, 200)).Gap(10).Children(func() {
				ui.Form(c, func() {
					ui.Field(c, T("名称", "Name"), func() {
						ui.TextInput(c.Key("name"), &e.name).Placeholder(pick(e.provider == provOpenAI, T("必填，如 openrouter", "Required, e.g. openrouter"), T("可选", "Optional")))
					})
					ui.Field(c, "base-url", func() {
						ui.TextInput(c.Key("base-url"), &e.baseURL).Placeholder(pick(e.provider == provOpenAI, "https://…/v1", T("留空使用官方地址", "Empty for the official endpoint")))
					})
					ui.Field(c, T("模型前缀 prefix", "Model prefix"), func() {
						ui.TextInput(c.Key("prefix"), &e.prefix).Placeholder(T("可选，如 team-a", "Optional, e.g. team-a"))
					})
					ui.Field(c, "proxy-url", func() {
						ui.TextInput(c.Key("proxy"), &e.proxy).Placeholder(T("可选；direct 表示不走代理", "Optional; direct bypasses proxies"))
					})
					ui.Field(c, T("优先级 priority", "Priority"), func() {
						ui.TextInput(c.Key("priority"), &e.priority).Placeholder("0")
					})
				})
				if e.provider == provOpenAI {
					ui.Checkbox(c.Key("disabled"), &e.disabled, T("停用此提供商（disabled）", "Disable this provider (disabled)"))
				}

				ui.Text(c, T("密钥", "Keys")).FontWeight(600).FontSize(13)
				for i, k := range e.keys {
					ui.Row(c).Key(fmt.Sprint("k", i)).Gap(6).AlignItems(ui.Center).Children(func() {
						in := ui.TextInput(c.Key(fmt.Sprint("key", i)), &k.key).Label(T("密钥", "Key")).Grow(2).Placeholder("api-key")
						if !k.show {
							in.Password()
						}
						ui.TextInput(c.Key(fmt.Sprint("weight", i)), &k.weight).Label("weight").Width(70).Placeholder("weight")
						ui.TextInput(c.Key(fmt.Sprint("kproxy", i)), &k.proxy).Label("proxy-url").Grow(1).Placeholder("proxy-url")
						ui.Checkbox(c.Key(fmt.Sprint("kshow", i)), &k.show, T("显示", "Show"))
						if ui.Button(c.Key(fmt.Sprint("kdel", i)), "✕").Label(T("移除此密钥", "Remove this key")).Clicked() {
							e.keys = slices.Delete(e.keys, i, i+1)
						}
					})
				}
				if ui.Button(c, T("添加密钥", "Add Key")).Clicked() {
					e.keys = append(e.keys, &provKeyRow{raw: rawObj{}, show: true})
				}

				ui.Text(c, T("模型映射（名称 → 别名）", "Models (name → alias)")).FontWeight(600).FontSize(13)
				for i, m := range e.models {
					ui.Row(c).Key(fmt.Sprint("m", i)).Gap(6).AlignItems(ui.Center).Children(func() {
						ui.TextInput(c.Key(fmt.Sprint("mname", i)), &m.name).Label(T("上游模型名", "Upstream model")).Grow(1).Placeholder(T("上游模型名", "Upstream model"))
						ui.TextInput(c.Key(fmt.Sprint("malias", i)), &m.alias).Label(T("别名", "Alias")).Grow(1).Placeholder(T("客户端别名", "Client alias"))
						if ui.Button(c.Key(fmt.Sprint("mdel", i)), "✕").Label(T("移除此模型", "Remove this model")).Clicked() {
							e.models = slices.Delete(e.models, i, i+1)
						}
					})
				}
				if ui.Button(c, T("添加模型", "Add Model")).Clicked() {
					e.models = append(e.models, &provModelRow{raw: rawObj{}})
				}

				ui.Text(c, T("排除的模型（每行一个，支持 * 通配）", "Excluded models (one per line, * wildcards)")).FontWeight(600).FontSize(13)
				ui.TextArea(c.Key("excluded"), &e.excluded).Height(70).Font("monospace").FontSize(12)
				ui.Text(c, T("自定义请求头（每行 名称: 值）", "Custom headers (one Name: value per line)")).FontWeight(600).FontSize(13)
				ui.TextArea(c.Key("headers"), &e.headers).Height(70).Font("monospace").FontSize(12)

				if len(e.extras) > 0 {
					ui.Text(c, fmt.Sprintf(T("以下字段此处不可编辑，保存时原样保留：%s", "These fields are not editable here and are kept as they are: %s"), strings.Join(e.extras, ", "))).FontSize(11).TextColor(t.TextMuted)
					ui.Row(c).Children(func() {
						if PillButton(c, icFileCog, T("在 YAML 中编辑", "Edit as YAML"), false).Clicked() {
							e.open = false
							cfgOpenYAML(env)
						}
					})
				}
			})

			problem := e.check()
			if problem != "" {
				ui.Text(c, problem).FontSize(12).TextColor(t.Danger)
			}
			ErrorBanner(c, st.op.Err)
			if busy {
				Busy(c, T("正在保存…", "Saving…"))
			}
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Text(c, T("目标实例：", "Target: ")+env.InstanceName()).FontSize(12).TextColor(t.TextMuted)
				ui.Box(c).Grow(1)
				if ui.Button(c, T("取消", "Cancel")).Disabled(busy).Clicked() {
					e.open = false
				}
				if ui.PrimaryButton(c, T("保存", "Save")).Disabled(busy || problem != "").Clicked() {
					e.save(env, st)
				}
			})
		})
	})
}

func (e *provEditor) save(env *PageEnv, st *providersState) {
	group, err := e.build()
	if err != nil {
		st.op.Err = err
		return
	}
	idx, orig := e.index, e.raw
	desc := fmt.Sprintf(T("编辑分组“%s”", "Edit group “%s”"), groupTitle(group))
	if idx < 0 {
		desc = fmt.Sprintf(T("添加分组“%s”", "Add group “%s”"), groupTitle(group))
	}
	st.writeList(env, e.provider, desc, func(fresh []rawObj) ([]rawObj, error) {
		next := slices.Clone(fresh)
		for i := range next {
			next[i] = withoutAuthIndex(next[i])
		}
		if idx < 0 {
			return append(next, group), nil
		}
		at := locateGroup(next, idx, orig)
		if at < 0 {
			return nil, errGroupGone
		}
		next[at] = group
		return next, nil
	}, func() { e.open = false })
}

func providerKindOf(id string) provKind {
	for _, k := range providerKinds {
		if k.id == id {
			return k
		}
	}
	return provKind{id, id, id}
}

// ---- Page ----

// provLogo is the provider whose logo heads a family's panel.
func provLogo(id string) string {
	if id == "interactions" {
		return "gemini"
	}
	return id
}

func providersPage(c *ui.Context, env *PageEnv) {
	t := c.Theme()
	st := State(env, "providers", newProvidersState)
	PageHeader(c, T("上游提供商", "Upstream Providers"), T("API 密钥类上游（api-keys）。一个分组共用一个地址和设置，下含多个密钥。OAuth 账号请在“账号”页管理。", "API-key upstreams (api-keys). A group shares one endpoint and its settings and holds several keys. OAuth accounts are managed on the Accounts page."), func() {
		if BusyPillButton(c, icRefresh, T("重新加载", "Reload"), st.load.Running, env.Client == nil || st.op.Running).Clicked() {
			st.dropConflict(env)
		}
	})
	if env.Client == nil {
		return
	}
	if !st.started {
		st.reload(env)
	}
	if st.load.Running && !st.loaded {
		Busy(c, T("正在读取提供商…", "Reading providers…"))
	}
	if !st.load.Running {
		ErrorBanner(c, st.load.Err)
	}
	if st.cf != nil {
		providerConflictPanel(c, env, st)
	} else {
		warnBanner(c, st.conflict)
	}
	if st.editor == nil || !st.editor.open {
		ErrorBanner(c, st.op.Err)
	}
	if st.op.Running && (st.editor == nil || !st.editor.open) {
		Busy(c, T("正在保存…", "Saving…"))
	}
	if !st.loaded {
		return
	}

	for _, kind := range providerKinds {
		groups := st.groups[kind.id]
		Panel(c).Key("p-" + kind.id).Children(func() {
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Column(c).Grow(1).Children(func() {
					ProviderPanelTitle(c, provLogo(kind.id), kind.title(), fmt.Sprintf(T("%d 个分组", "%d groups"), len(groups)))
				})
				if PillButton(c.Key("add-"+kind.id), icPlus, T("添加分组", "Add Group"), st.op.Running).Clicked() {
					st.editor = newProvEditor(kind.id, -1, rawObj{})
					if kind.id != provOpenAI {
						st.editor.keys = []*provKeyRow{{raw: rawObj{}, show: true}}
					}
				}
			})
			if len(groups) == 0 {
				ui.Text(c, T("没有配置。", "None configured.")).FontSize(12).TextColor(t.TextMuted)
			}
			for gi, g := range groups {
				providerGroupCard(c, env, st, kind, gi, g)
			}
		})
	}

	if st.delete.open {
		var g rawObj
		if gs := st.groups[st.delete.provider]; st.delete.index < len(gs) {
			g = gs[st.delete.index]
		}
		if g == nil {
			st.delete.open = false
		} else {
			msg := fmt.Sprintf(T("将从实例“%s”删除 %s 分组“%s”（%s），其中 %d 个密钥一并删除，无法恢复。", "Group “%[3]s” (%[4]s) of %[2]s will be deleted from instance “%[1]s” with its %[5]d keys. This cannot be undone."),
				env.InstanceName(), providerKindOf(st.delete.provider).title(), groupTitle(g), rawString(g, "base-url"), len(rawObjs(g, "keys")))
			if ui.AlertDialog(c, &st.delete.open, T("删除分组？", "Delete group?"), msg, T("取消", "Cancel"), T("确认删除", "Confirm Delete")) == 1 {
				idx, provider := st.delete.index, st.delete.provider
				desc := fmt.Sprintf(T("删除分组“%s”", "Delete group “%s”"), groupTitle(g))
				st.writeList(env, provider, desc, func(fresh []rawObj) ([]rawObj, error) {
					at := locateGroup(fresh, idx, g)
					if at < 0 {
						return nil, errGroupGone
					}
					next := slices.Delete(slices.Clone(fresh), at, at+1)
					for i := range next {
						next[i] = withoutAuthIndex(next[i])
					}
					return next, nil
				}, func() {})
			}
		}
	}
	if st.editor != nil && st.editor.open {
		st.editor.view(c, env, st)
	}
}

// providerConflictPanel tells that the server's list of a provider changed
// while the user was editing, shows what changed, and offers the two ways
// on. Nothing is written until the user picks one and, for keeping their
// edit, confirms.
func providerConflictPanel(c *ui.Context, env *PageEnv, st *providersState) {
	t := c.Theme()
	cf := st.cf
	Panel(c).Key("conflict").Children(func() {
		PanelTitle(c, icAlert, TintOrange, T("服务器上的内容已变化", "The server's content changed"), providerKindOf(cf.provider).title(), false)
		ui.Text(c, st.conflict).FontSize(12).TextColor(t.TextMuted)
		ui.Text(c, T("你的修改：", "Your change: ")+cf.desc).FontSize(13).FontWeight(500)
		ui.Text(c, T("自你载入以来的变化（已载入 → 服务器当前，密钥已遮蔽）", "Changes since you loaded it (loaded → on the server now; keys masked)")).FontSize(12).TextColor(t.TextMuted)
		diffView(c, cf.diff)
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Box(c).Grow(1)
			if PillButton(c, icRefresh, T("放弃我的修改并重新加载", "Discard My Changes and Reload"), st.op.Running).Clicked() {
				st.dropConflict(env)
			}
			if ui.PrimaryButton(c, T("保留我的修改并重试", "Keep My Changes and Retry")).Disabled(st.op.Running).Clicked() {
				st.confirmRetry = true
			}
		})
	})
	if st.confirmRetry {
		msg := fmt.Sprintf(T("将把你的修改（%[1]s）应用到实例“%[2]s”上 %[3]s 的当前内容之上并写入。服务器上其它分组的改动会保留；你修改的分组以你的版本为准。", "Your change (%[1]s) will be applied on top of the current %[3]s content of instance “%[2]s” and written. Changes on the server to other groups are kept; the group you edited takes your version."),
			cf.desc, env.InstanceName(), providerKindOf(cf.provider).title())
		if ui.AlertDialog(c, &st.confirmRetry, T("保留你的修改并写入？", "Keep your change and write it?"), msg, T("取消", "Cancel"), T("确认重试", "Confirm Retry")) == 1 {
			st.retryConflict(env)
		}
	}
}

func groupTitle(g rawObj) string {
	switch {
	case rawString(g, "name") != "":
		return rawString(g, "name")
	case rawString(g, "base-url") != "":
		return rawString(g, "base-url")
	}
	return T("(未命名)", "(unnamed)")
}

// provModelsSummary tells a group's model mappings and exclusions in a line.
func provModelsSummary(g rawObj) string {
	models := rawObjs(g, "models")
	var parts []string
	for i, m := range models {
		if i == 3 {
			parts = append(parts, fmt.Sprintf(T("等 %d 个", "+%d more"), len(models)-3))
			break
		}
		name, alias := rawString(m, "name"), rawString(m, "alias")
		if alias != "" && alias != name {
			name += " → " + alias
		}
		parts = append(parts, name)
	}
	out := T("无模型映射", "No model mappings")
	if len(parts) > 0 {
		out = strings.Join(parts, T("，", ", "))
	}
	if n := len(rawStrings(g, "excluded-models")); n > 0 {
		out += fmt.Sprintf(T("；排除 %d 个", "; %d excluded"), n)
	}
	return out
}

func providerGroupCard(c *ui.Context, env *PageEnv, st *providersState, kind provKind, gi int, g rawObj) {
	t := c.Theme()
	id := fmt.Sprintf("%s-%d", kind.id, gi)
	ui.Column(c).Key(id).Radius(14).Background(pal(t).Inset).Padding(0, 0, 4, 0).Children(func() {
		ui.Row(c).Gap(8).AlignItems(ui.Center).Padding(12, 14, 6, 14).Children(func() {
			ui.Text(c, groupTitle(g)).FontSize(14).FontWeight(600).SingleLine()
			if rawBool(g, "disabled") {
				Pill(c, T("已停用", "Disabled"), t.TextMuted)
			}
			if slices.Contains(rawStrings(g, "excluded-models"), "*") {
				Pill(c, T("已排除全部模型", "All models excluded"), t.Warning)
			}
			ui.Box(c).Grow(1)
			if PillButton(c.Key("edit-"+id), cfgIcEdit, T("编辑", "Edit"), st.op.Running).Clicked() {
				st.editor = newProvEditor(kind.id, gi, g)
			}
			if cfgDangerPill(c.Key("del-"+id), icTrash, T("删除", "Delete"), st.op.Running).Clicked() {
				st.delete.provider, st.delete.index, st.delete.open = kind.id, gi, true
			}
		})
		ui.Column(c).Gap(3).Padding(0, 14, 8, 14).Children(func() {
			if u := rawString(g, "base-url"); u != "" {
				kv(c, "base-url", u)
			}
			if p := rawString(g, "prefix"); p != "" {
				kv(c, T("模型前缀", "Prefix"), p)
			}
			if p := rawString(g, "proxy-url"); p != "" {
				kv(c, "proxy-url", p)
			}
			kv(c, T("模型", "Models"), provModelsSummary(g))
		})
		for ki, k := range rawObjs(g, "keys") {
			secret := rawString(k, "api-key")
			rid := fmt.Sprintf("%s-%d", id, ki)
			ui.Box(c).Height(1).Margin(0, 14).Background(pal(t).Line)
			ui.Row(c).Key(rid).Gap(8).Padding(6, 14).AlignItems(ui.Center).Children(func() {
				text := cfgMask(secret)
				if st.reveal[rid] {
					text = secret
				}
				ui.Text(c, text).Font("monospace").FontSize(12).SingleLine().Grow(1).Selectable()
				if w := rawNumber(k, "weight"); w != "" {
					Pill(c, "weight "+w, t.Accent)
				}
				if p := rawString(k, "proxy-url"); p != "" {
					Pill(c, p, t.TextMuted)
				}
				show, icon := T("显示", "Show"), cfgIcEye
				if st.reveal[rid] {
					show, icon = T("隐藏", "Hide"), cfgIcEyeOff
				}
				if PillButton(c.Key("show-"+rid), icon, show, false).Clicked() {
					st.reveal[rid] = !st.reveal[rid]
				}
				if PillButton(c.Key("copy-"+rid), icCopy, T("复制", "Copy"), false).Clicked() {
					c.WriteClipboard(secret)
					c.Toast(T("已复制密钥", "Key copied"))
				}
			})
		}
	})
}
