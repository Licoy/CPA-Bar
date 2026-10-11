package view

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/licoy/cpa-bar/internal/cpa"
)

// Models & routing edits, at their v8 paths (config.example.yaml):
//
//	routing.strategy, routing.force-model-prefix, routing.session-affinity,
//	routing.session-affinity-ttl, routing.session-affinity-subagents
//	oauth.excluded-models.<channel>   list of model patterns
//	oauth.model-alias.<channel>       list of {name, alias, fork, ...}
//
// The three of them that are too nested for a form (oauth.request-scoped-errors,
// oauth.settings, requests.payload) are shown read-only. Models of API-key
// upstream groups (their aliases and exclusions) belong to the providers page.

func init() {
	RegisterPage(Page{
		ID:       "routing",
		Order:    12,
		Group:    GroupManage,
		Title:    func() string { return T("模型与路由", "Models & Routing") },
		Instance: true,
		View:     routingPage,
	})
}

// routingStrategies are the values the server accepts (RoutingConfig.Strategy).
var routingStrategies = []string{"round-robin", "weighted-round-robin", "fill-first"}

// oauthChannels are the channels the example configuration lists; channels
// found on the server are added to them.
var oauthChannels = []string{"vertex", "aistudio", "antigravity", "claude", "codex", "kimi", "xai", "meta"}

func routingFields() []*cfgField {
	return []*cfgField{
		{path: []string{"routing", "strategy"}, kind: cfgEnum, options: routingStrategies, def: "round-robin",
			optionsZh: []string{"轮询", "加权轮询", "用满再换"}, optionsEn: []string{"Round-robin", "Weighted", "Fill-first"},
			zh: "账号选择策略", en: "Credential selection", helpZh: "round-robin 轮流；weighted-round-robin 按账号权重；fill-first 用满一个再用下一个", helpEn: "round-robin takes turns; weighted-round-robin follows credential weights; fill-first uses one until it is exhausted"},
		{path: []string{"routing", "force-model-prefix"}, kind: cfgBool, def: "false",
			zh: "强制模型前缀", en: "Force model prefix", helpZh: "开启后，不带前缀的请求只会用没有前缀的账号", helpEn: "When on, requests without a prefix only use credentials without one"},
		{path: []string{"routing", "session-affinity"}, kind: cfgBool, def: "false",
			zh: "会话亲和", en: "Session affinity", helpZh: "同一会话尽量使用同一个账号", helpEn: "Keep a session on the same credential"},
		{path: []string{"routing", "session-affinity-ttl"}, kind: cfgText,
			zh: "会话绑定保留时间", en: "Session binding TTL", helpZh: "如 30m、1h；留空为服务器默认 1h", helpEn: "e.g. 30m, 1h; empty uses the server default of 1h"},
		{path: []string{"routing", "session-affinity-subagents"}, kind: cfgBool, def: "true",
			zh: "子代理沿用父会话账号", en: "Subagents follow the parent", helpZh: "仅在开启会话亲和时有效，服务器默认开启", helpEn: "Only with session affinity on; the server defaults to on"},
	}
}

// ---- State ----

type exclEditor struct {
	canon string // of the server's list when loaded
	base  string // its lines, joined
	text  string
}

type aliasRow struct {
	raw           rawObj
	name, alias   string
	fork          bool
	oname, oalias string
	ofork         bool
}

type aliasEditor struct {
	canon string
	// orig is the server's list as loaded, for reverting.
	orig json.RawMessage
	rows []*aliasRow
	// base is the rows as loaded, for telling whether they changed.
	base string
}

func (a *aliasEditor) signature() string {
	var sb strings.Builder
	for _, r := range a.rows {
		fmt.Fprintf(&sb, "%s\x00%s\x00%v\n", strings.TrimSpace(r.name), strings.TrimSpace(r.alias), r.fork)
	}
	return sb.String()
}

type routingState struct {
	form *cfgForm

	started bool
	loaded  bool
	load    Task
	op      Task

	channels []string
	excl     map[string]*exclEditor
	alias    map[string]*aliasEditor
	scoped   json.RawMessage
	settings json.RawMessage
	payload  json.RawMessage

	conflict string

	// added are the channels the user added that the server does not have
	// yet; newChannel is the name being typed.
	added      map[string]bool
	newChannel string
}

func newRoutingState() *routingState {
	return &routingState{form: newCfgForm(routingFields()), excl: map[string]*exclEditor{}, alias: map[string]*aliasEditor{}, added: map[string]bool{}}
}

// channelPattern is what a channel name may be: the server keeps channel
// names in lower case, and a name is a path segment of the management API.
var channelPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// validateChannel says why name cannot be a new channel, "" when it can.
func validateChannel(name string, existing []string) string {
	switch {
	case name == "":
		return T("请输入渠道名称", "Enter a channel name")
	case !channelPattern.MatchString(name):
		return T("渠道名称只能含小写字母、数字、- _ .，以字母或数字开头，最长 64 个字符", "A channel name takes lowercase letters, digits, - _ . only, starts with a letter or digit, and has at most 64 characters")
	case slices.Contains(existing, name):
		return T("该渠道已存在", "This channel already exists")
	}
	return ""
}

// sortChannels puts the known channels first, in their order, then the others
// by name.
func sortChannels(chs []string) {
	sort.SliceStable(chs, func(i, j int) bool {
		a, b := slices.Index(oauthChannels, chs[i]), slices.Index(oauthChannels, chs[j])
		switch {
		case a >= 0 && b >= 0:
			return a < b
		case a >= 0:
			return true
		case b >= 0:
			return false
		}
		return chs[i] < chs[j]
	})
}

// addChannel makes a new, empty channel to fill in and save: nothing is
// written until the user saves one of its lists.
func (s *routingState) addChannel(name string) {
	s.added[name] = true
	s.channels = append(s.channels, name)
	sortChannels(s.channels)
	s.excl[name] = newExclEditor(nil)
	s.alias[name] = newAliasEditor(nil)
}

// isNew says whether the server has nothing of the channel yet.
func (s *routingState) isNew(ch string) bool {
	return s.added[ch] && s.excl[ch] != nil && s.excl[ch].canon == "" && s.alias[ch] != nil && s.alias[ch].canon == ""
}

func newAliasEditor(raw json.RawMessage) *aliasEditor {
	a := &aliasEditor{canon: cfgCanon(raw), orig: raw}
	var rows []rawObj
	if !rawIsNull(raw) {
		_ = json.Unmarshal(raw, &rows)
	}
	for _, r := range rows {
		row := &aliasRow{raw: r, name: rawString(r, "name"), alias: rawString(r, "alias"), fork: rawBool(r, "fork")}
		row.oname, row.oalias, row.ofork = row.name, row.alias, row.fork
		a.rows = append(a.rows, row)
	}
	a.base = a.signature()
	return a
}

func newExclEditor(raw json.RawMessage) *exclEditor {
	var l []string
	if !rawIsNull(raw) {
		_ = json.Unmarshal(raw, &l)
	}
	text := strings.Join(l, "\n")
	return &exclEditor{canon: cfgCanon(raw), base: text, text: text}
}

func (s *routingState) reload(env *PageEnv) {
	if env.Client == nil {
		return
	}
	s.started = true
	cl := env.Client
	var excl, alias map[string]json.RawMessage
	var scoped, settings, payload json.RawMessage
	env.Run(&s.load, func(ctx context.Context) error {
		get := func(dst *map[string]json.RawMessage, path ...string) error {
			raw, err := cfgGet(ctx, cl, path...)
			if err != nil {
				return err
			}
			*dst = nil
			if !rawIsNull(raw) {
				if err := json.Unmarshal(raw, dst); err != nil {
					return fmt.Errorf("%s", T("无法解析路由配置", "Could not parse the routing configuration"))
				}
			}
			return nil
		}
		if err := get(&excl, "oauth", "excluded-models"); err != nil {
			return err
		}
		if err := get(&alias, "oauth", "model-alias"); err != nil {
			return err
		}
		var err error
		if scoped, err = cfgGet(ctx, cl, "oauth", "request-scoped-errors"); err != nil {
			return err
		}
		if settings, err = cfgGet(ctx, cl, "oauth", "settings"); err != nil {
			return err
		}
		payload, err = cfgGet(ctx, cl, "requests", "payload")
		return err
	}, func(err error) {
		if err != nil {
			return
		}
		set := map[string]bool{}
		for _, ch := range oauthChannels {
			set[ch] = true
		}
		for ch := range excl {
			set[ch] = true
			delete(s.added, ch)
		}
		for ch := range alias {
			set[ch] = true
			delete(s.added, ch)
		}
		for ch := range s.added {
			set[ch] = true
		}
		s.channels = s.channels[:0]
		for ch := range set {
			s.channels = append(s.channels, ch)
		}
		sortChannels(s.channels)
		// Edits not saved yet survive a reload that finds the server's list
		// as it was; a list that changed on the server shows the server's.
		prevExcl, prevAlias := s.excl, s.alias
		s.excl, s.alias = map[string]*exclEditor{}, map[string]*aliasEditor{}
		for _, ch := range s.channels {
			ne, na := newExclEditor(excl[ch]), newAliasEditor(alias[ch])
			if old := prevExcl[ch]; old != nil && old.canon == ne.canon {
				ne.text = old.text
			}
			if old := prevAlias[ch]; old != nil && old.canon == na.canon {
				na.rows = old.rows
			}
			s.excl[ch], s.alias[ch] = ne, na
		}
		s.scoped, s.settings, s.payload = scoped, settings, payload
		s.loaded = true
	})
}

// saveNode replaces the node at path with body (deletes it when body is nil)
// unless the server's node is no longer the one shown; reset gets the
// server's node then.
func (s *routingState) saveNode(env *PageEnv, path []string, baseline string, body json.RawMessage, reset func(latest json.RawMessage)) {
	cl := env.Client
	var latest json.RawMessage
	env.Run(&s.op, func(ctx context.Context) error {
		raw, err := cfgGet(ctx, cl, path...)
		if err != nil {
			return err
		}
		if cfgCanon(raw) != baseline {
			latest = raw
			return &cfgChanged{}
		}
		if body == nil {
			if raw == nil {
				return nil
			}
			if err := cl.DeleteConfig(ctx, path...); cpa.KindOf(err) != cpa.KindNotFound {
				return err
			}
			return nil
		}
		return cl.PutConfig(ctx, body, path...)
	}, func(err error) {
		var ch *cfgChanged
		switch {
		case errors.As(err, &ch):
			reset(latest)
			s.conflict = ch.Error()
			s.op.Err = nil
		case err == nil:
			s.conflict = ""
			s.reload(env)
		case IsIndeterminate(err):
			s.reload(env)
		}
	})
}

// saveExcluded writes the excluded models of a channel: the list as lines,
// or no list at all when there are none.
func (s *routingState) saveExcluded(env *PageEnv, ch string) {
	ed := s.excl[ch]
	var body json.RawMessage
	if l := linesOf(ed.text); len(l) > 0 {
		body = jsonRaw(l)
	}
	s.saveNode(env, []string{"oauth", "excluded-models", ch}, ed.canon, body, func(latest json.RawMessage) {
		s.excl[ch] = newExclEditor(latest)
	})
}

// saveAliases writes the model aliases of a channel, keeping the fields of an
// entry that the page does not edit.
func (s *routingState) saveAliases(env *PageEnv, ch string) {
	ed := s.alias[ch]
	list := []rawObj{}
	for _, r := range ed.rows {
		if strings.TrimSpace(r.name) == "" {
			continue
		}
		m := cloneRaw(r.raw)
		m["name"] = jsonRaw(strings.TrimSpace(r.name))
		m["alias"] = jsonRaw(strings.TrimSpace(r.alias))
		if r.fork {
			m["fork"] = jsonRaw(true)
		} else {
			delete(m, "fork")
		}
		list = append(list, m)
	}
	var body json.RawMessage
	if len(list) > 0 {
		body = jsonRaw(list)
	}
	s.saveNode(env, []string{"oauth", "model-alias", ch}, ed.canon, body, func(latest json.RawMessage) {
		s.alias[ch] = newAliasEditor(latest)
	})
}

// ---- Page ----

func routingPage(c *ui.Context, env *PageEnv) {
	t := c.Theme()
	st := State(env, "routing", newRoutingState)
	PageHeader(c, T("模型与路由", "Models & Routing"), T("账号选择策略、OAuth 渠道的模型排除与别名。API 密钥上游的模型映射在“上游提供商”页。", "Credential selection, and model exclusions and aliases of OAuth channels. Model mappings of API-key upstreams are on the Upstream Providers page."), func() {
		if BusyPillButton(c, icRefresh, T("重新加载", "Reload"), st.load.Running || st.form.load.Running, env.Client == nil || st.op.Running).Clicked() {
			st.reload(env)
			st.form.reload(env, false)
		}
	})
	if env.Client == nil {
		return
	}
	if !st.started {
		st.reload(env)
	}
	if !st.form.started {
		st.form.reload(env, false)
	}

	st.form.view(c, env, []cfgSection{{zh: "路由策略", en: "Routing strategy", from: 0, to: len(st.form.vals), icon: icRoute, tint: TintBlue}})

	if st.load.Running && !st.loaded {
		Busy(c, T("正在读取模型配置…", "Reading model settings…"))
	}
	if !st.load.Running {
		ErrorBanner(c, st.load.Err)
	}
	warnBanner(c, st.conflict)
	ErrorBanner(c, st.op.Err)
	if st.op.Running {
		Busy(c, T("正在保存…", "Saving…"))
	}
	if !st.loaded {
		return
	}

	routingAddChannel(c, st)

	Panel(c).Key("excluded").Children(func() {
		PanelTitle(c, icBan, TintOrange, T("OAuth 渠道的模型排除", "Excluded models per OAuth channel"), fmt.Sprintf(T("%d 个渠道", "%d channels"), len(st.channels)), false)
		ui.Text(c, T("每行一个模型，支持 * 通配（如 gemini-2.5-*、*-preview）。写入 oauth.excluded-models.<渠道>。", "One model per line, * wildcards allowed (e.g. gemini-2.5-*, *-preview). Written to oauth.excluded-models.<channel>.")).FontSize(12).TextColor(t.TextMuted)
		for _, ch := range st.channels {
			ed := st.excl[ch]
			ui.Column(c).Key("excl-"+ch).Padding(12, 14).Gap(8).Radius(14).Background(pal(t).Inset).Children(func() {
				ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
					routingChannelHead(c, st, ch, len(linesOf(ed.text)))
					changed := strings.Join(linesOf(ed.text), "\n") != strings.Join(linesOf(ed.base), "\n")
					if PillButton(c.Key("excl-reset-"+ch), cfgIcUndo, T("还原", "Revert"), !changed || st.op.Running).Clicked() {
						ed.text = ed.base
					}
					if ui.PrimaryButton(c.Key("excl-save-"+ch), T("保存", "Save")).Disabled(!changed || st.op.Running).
						Tooltip(T("写入到实例：", "Writes to instance: ") + env.InstanceName()).Clicked() {
						st.saveExcluded(env, ch)
					}
				})
				ui.TextArea(c.Key("excl-text-"+ch), &ed.text).Label(ch).Height(72).Font("monospace").FontSize(12)
			})
		}
		ui.Text(c, T("目标实例：", "Target: ")+env.InstanceName()).FontSize(12).TextColor(t.TextMuted)
	})

	Panel(c).Key("aliases").Children(func() {
		PanelTitle(c, icRoute, TintPurple, T("OAuth 渠道的模型别名", "Model aliases per OAuth channel"), fmt.Sprintf(T("%d 个渠道", "%d channels"), len(st.channels)), false)
		ui.Text(c, T("把上游模型名映射成客户端可见的别名；“保留原名”表示别名之外原模型仍可用。写入 oauth.model-alias.<渠道>。", "Map an upstream model name to the alias clients see; “Keep original” leaves the original model available too. Written to oauth.model-alias.<channel>.")).FontSize(12).TextColor(t.TextMuted)
		for _, ch := range st.channels {
			ed := st.alias[ch]
			ui.Column(c).Key("alias-"+ch).Padding(12, 14).Gap(8).Radius(14).Background(pal(t).Inset).Children(func() {
				ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
					routingChannelHead(c, st, ch, len(ed.rows))
					changed := ed.signature() != ed.base
					bad := false
					for _, r := range ed.rows {
						if (strings.TrimSpace(r.name) == "") != (strings.TrimSpace(r.alias) == "") {
							bad = true
						}
					}
					if bad {
						ui.Text(c, T("名称与别名需成对填写", "Fill name and alias together")).FontSize(11).TextColor(t.Danger)
					}
					if PillButton(c.Key("alias-reset-"+ch), cfgIcUndo, T("还原", "Revert"), !changed || st.op.Running).Clicked() {
						st.alias[ch] = newAliasEditor(ed.orig)
					}
					if ui.PrimaryButton(c.Key("alias-save-"+ch), T("保存", "Save")).Disabled(!changed || bad || st.op.Running).
						Tooltip(T("写入到实例：", "Writes to instance: ") + env.InstanceName()).Clicked() {
						st.saveAliases(env, ch)
					}
				})
				for i, r := range ed.rows {
					ui.Row(c).Key(fmt.Sprint(ch, i)).Gap(6).AlignItems(ui.Center).Children(func() {
						ui.TextInput(c.Key(fmt.Sprint("an", ch, i)), &r.name).Label(T("上游模型名", "Upstream model")).Grow(1).Placeholder(T("上游模型名", "Upstream model"))
						ui.TextInput(c.Key(fmt.Sprint("aa", ch, i)), &r.alias).Label(T("别名", "Alias")).Grow(1).Placeholder(T("客户端别名", "Client alias"))
						ui.Checkbox(c.Key(fmt.Sprint("af", ch, i)), &r.fork, T("保留原名", "Keep original"))
						if ui.Button(c.Key(fmt.Sprint("ad", ch, i)), "✕").Label(T("移除此映射", "Remove this mapping")).Clicked() {
							ed.rows = slices.Delete(ed.rows, i, i+1)
						}
					})
				}
				ui.Row(c).Children(func() {
					if PillButton(c.Key("alias-add-"+ch), icPlus, T("添加映射", "Add Mapping"), false).Clicked() {
						ed.rows = append(ed.rows, &aliasRow{raw: rawObj{}})
					}
				})
			})
		}
	})

	Panel(c).Key("readonly").Children(func() {
		PanelTitle(c, icFileCog, TintGray, T("更多（只读）", "More (read-only)"), "", false)
		ui.Text(c, T("以下设置结构较复杂，此处只读；点击“在 YAML 中编辑”修改。", "These settings are too nested for a form; they are shown read-only. Use “Edit as YAML” to change them.")).FontSize(12).TextColor(t.TextMuted)
	})
	cfgJSONBlock(c, env, "oauth.request-scoped-errors", st.scoped)
	cfgJSONBlock(c, env, "oauth.settings", st.settings)
	cfgJSONBlock(c, env, "requests.payload", st.payload)
}

// routingChannelHead names a channel in the header row of its block, with
// how many entries it holds; a channel the server does not have yet can be
// dropped again.
func routingChannelHead(c *ui.Context, st *routingState, ch string, count int) {
	t := c.Theme()
	ProviderIcon(c, ch, 18)
	ui.Text(c, providerLabel(ch)).FontSize(14).FontWeight(600).SingleLine()
	if providerLabel(ch) != ch {
		ui.Text(c, ch).FontSize(11).TextColor(t.TextMuted).SingleLine()
	}
	Pill(c, fmt.Sprint(count), pick(count > 0, t.Accent, t.TextMuted))
	if st.isNew(ch) {
		Pill(c, T("新渠道", "New"), t.Success)
		if PillButton(c.Key("drop-"+ch), nil, T("移除", "Remove"), false).Clicked() {
			delete(st.added, ch)
			delete(st.excl, ch)
			delete(st.alias, ch)
			st.channels = slices.DeleteFunc(st.channels, func(x string) bool { return x == ch })
		}
	}
	ui.Box(c).Grow(1)
}

// routingAddChannel is the panel that adds a channel the page does not list,
// for the exclusions and aliases of an OAuth channel with a name of its own.
func routingAddChannel(c *ui.Context, st *routingState) {
	t := c.Theme()
	Panel(c).Key("add-channel").Children(func() {
		PanelTitle(c, icPlus, TintGreen, T("自定义渠道", "Custom channel"), "", false)
		name := strings.TrimSpace(st.newChannel)
		problem := validateChannel(name, st.channels)
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.TextInput(c.Key("new-channel"), &st.newChannel).Label(T("渠道名称", "Channel name")).Grow(1).Placeholder(T("如 my-channel（小写字母、数字、- _ .）", "e.g. my-channel (lowercase letters, digits, - _ .)"))
			if PillButton(c, icPlus, T("添加渠道", "Add Channel"), problem != "").Clicked() {
				st.addChannel(name)
				st.newChannel = ""
			}
		})
		switch {
		case name != "" && problem != "":
			ui.Text(c, problem).FontSize(12).TextColor(t.Danger)
		default:
			ui.Text(c, T("列表里没有的渠道可以在这里添加；添加后在下面填写并保存才会写入服务器。", "Add a channel the lists do not have here; it is written to the server when you fill in and save one of its lists below.")).FontSize(12).TextColor(t.TextMuted)
		}
	})
}
