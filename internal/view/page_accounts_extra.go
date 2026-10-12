package view

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/licoy/cpa-bar/internal/cpa"
)

// moreButton is an icon button that opens a menu of the system's.
func moreButton(c *ui.Context, label string, build func(m *ui.Menu)) ui.Element {
	t := c.Theme()
	b := ui.ButtonBase(c.Key("more")).Label(label).Size(30, 30).Radius(999).Justify(ui.Center).AlignItems(ui.Center)
	bg := t.Text.Alpha(0.06)
	switch {
	case b.Pressed():
		bg = t.Text.Alpha(0.14)
	case b.Hovered():
		bg = t.Text.Alpha(0.10)
	}
	return b.Background(bg).Children(func() {
		ui.Icon(c, icMore).FontSize(15).TextColor(t.Text)
	}).Menu(build)
}

// pinButton pins an account to the top of the lists, or unpins it. A
// pinned account's pin is filled, in the accent color.
func pinButton(c *ui.Context, pinned bool) ui.Element {
	t := c.Theme()
	label, icon, col := T("置顶", "Pin to Top"), icPin, t.Text
	if pinned {
		label, icon, col = T("取消置顶", "Unpin"), icPinned, t.Accent
	}
	b := ui.ButtonBase(c.Key("pin")).Label(label).Tooltip(label).Size(30, 30).Radius(999).Justify(ui.Center).AlignItems(ui.Center)
	bg := t.Text.Alpha(0.06)
	switch {
	case b.Pressed():
		bg = t.Text.Alpha(0.14)
	case b.Hovered():
		bg = t.Text.Alpha(0.10)
	case pinned:
		bg = t.Accent.Alpha(0.12)
	}
	return b.Background(bg).Children(func() {
		ui.Icon(c, icon).FontSize(14).TextColor(col)
	})
}

// ---- Editing an account ----

// headerRow is a custom request header being edited.
type headerRow struct {
	id       int
	key, val string
}

// accountEdit is the editor of an account's metadata: the priority and
// note, which the account list reports, and the prefix, proxy URL and
// custom headers, which it does not. The latter are read from the
// credential file when the user asks (see loadEditFields).
type accountEdit struct {
	open  bool
	key   string
	name  string
	title string

	priority, note         string
	origPriority, origNote string

	prefix, proxy         string
	origPrefix, origProxy string
	headers               []headerRow
	nextRow               int
	// origHeaders are the headers the server has, when loaded.
	origHeaders map[string]string
	// loaded: the three fields above carry the server's values, so that
	// emptying one clears it.
	loaded bool

	load Task
	save Task
}

// editableFields are the credential fields the editor reads.
type editableFields struct {
	prefix, proxy string
	headers       map[string]string
}

// readEditableFields picks the prefix, proxy URL and headers out of a
// credential's JSON, and nothing else: the rest of a credential file is
// tokens. found reports whether it had any of them.
func readEditableFields(raw []byte) (f editableFields, found bool) {
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil {
		return f, false
	}
	if v, ok := m["prefix"]; ok {
		found = true
		_ = json.Unmarshal(v, &f.prefix)
	}
	for _, k := range []string{"proxy_url", "proxy-url"} {
		if v, ok := m[k]; ok {
			found = true
			var s string
			if json.Unmarshal(v, &s) == nil && f.proxy == "" {
				f.proxy = s
			}
		}
	}
	if v, ok := m["headers"]; ok {
		found = true
		var hs map[string]any
		if json.Unmarshal(v, &hs) == nil {
			f.headers = map[string]string{}
			for k, hv := range hs {
				if s, ok := hv.(string); ok && strings.TrimSpace(k) != "" && strings.TrimSpace(s) != "" {
					f.headers[strings.TrimSpace(k)] = strings.TrimSpace(s)
				}
			}
		}
	}
	f.prefix, f.proxy = strings.TrimSpace(f.prefix), strings.TrimSpace(f.proxy)
	return f, found
}

// newAccountEdit opens the editor on cr. The account list does not carry
// the prefix, proxy URL and headers; if a server does report them, they
// are used.
func newAccountEdit(cr cpa.Credential) accountEdit {
	e := accountEdit{open: true, key: credKeyOf(cr), name: credFileName(cr), title: PrivName(cr.DisplayName()),
		priority: priorityText(cr.Priority), note: cr.Note, origPriority: priorityText(cr.Priority), origNote: cr.Note}
	if f, found := readEditableFields(cr.Raw); found {
		e.applyLoaded(f)
	}
	return e
}

// applyLoaded takes the server's values, keeping what the user already
// typed in the fields they touched.
func (e *accountEdit) applyLoaded(f editableFields) {
	if e.prefix == e.origPrefix {
		e.prefix = f.prefix
	}
	if e.proxy == e.origProxy {
		e.proxy = f.proxy
	}
	e.origPrefix, e.origProxy = f.prefix, f.proxy
	e.origHeaders = f.headers
	keys := make([]string, 0, len(f.headers))
	for k := range f.headers {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	var rows []headerRow
	for _, k := range keys {
		if !slices.ContainsFunc(e.headers, func(r headerRow) bool { return strings.TrimSpace(r.key) == k }) {
			e.nextRow++
			rows = append(rows, headerRow{id: e.nextRow, key: k, val: f.headers[k]})
		}
	}
	e.headers = append(rows, e.headers...)
	e.loaded = true
}

func (e *accountEdit) addHeader() {
	e.nextRow++
	e.headers = append(e.headers, headerRow{id: e.nextRow})
}

// editFields is the body of the edit: only the fields the user changed,
// and only the name to identify the credential. The server treats every
// other key as a field to write into the credential file, so no
// auth_index goes in.
//
// A cleared prefix or proxy URL is sent as "", which the server copies
// into the credential; null would leave the running credential's value.
// Headers are merged by the server: a header with a blank value is removed.
func editFields(e *accountEdit) (map[string]any, error) {
	fields := map[string]any{"name": e.name}
	if strings.TrimSpace(e.priority) != strings.TrimSpace(e.origPriority) {
		p, err := parsePriority(e.priority)
		if err != nil {
			return nil, fmt.Errorf("%s", T("优先级需为整数", "Priority must be an integer"))
		}
		if p == nil {
			fields["priority"] = nil
		} else {
			fields["priority"] = *p
		}
	}
	if strings.TrimSpace(e.note) != strings.TrimSpace(e.origNote) {
		if n := strings.TrimSpace(e.note); n == "" {
			fields["note"] = nil
		} else {
			fields["note"] = n
		}
	}
	if v := strings.TrimSpace(e.prefix); v != strings.TrimSpace(e.origPrefix) {
		fields["prefix"] = v
	}
	if v := strings.TrimSpace(e.proxy); v != strings.TrimSpace(e.origProxy) {
		fields["proxy_url"] = v
	}
	h, err := headerPatch(e)
	if err != nil {
		return nil, err
	}
	if len(h) > 0 {
		fields["headers"] = h
	}
	return fields, nil
}

// headerPatch is the headers to send: the new and changed ones, and "" for
// those to remove.
func headerPatch(e *accountEdit) (map[string]any, error) {
	cur := map[string]string{}
	for _, r := range e.headers {
		k, v := strings.TrimSpace(r.key), strings.TrimSpace(r.val)
		if k == "" {
			if v != "" {
				return nil, fmt.Errorf("%s", T("请求头需要名称", "A header needs a name"))
			}
			continue
		}
		if !validHeaderName(k) {
			return nil, fmt.Errorf(T("请求头名称无效：%s", "Invalid header name: %s"), k)
		}
		if _, dup := cur[k]; dup {
			return nil, fmt.Errorf(T("请求头重复：%s", "Duplicate header: %s"), k)
		}
		cur[k] = v
	}
	patch := map[string]any{}
	for k, v := range cur {
		old, had := e.origHeaders[k]
		switch {
		case v == "":
			// Before the server's headers are read, a blank value is the only
			// way to remove one.
			if had || !e.loaded {
				patch[k] = ""
			}
		case !had || old != v:
			patch[k] = v
		}
	}
	for k := range e.origHeaders {
		if _, ok := cur[k]; !ok {
			patch[k] = ""
		}
	}
	return patch, nil
}

// validHeaderName reports an HTTP token: letters, digits and the
// punctuation RFC 9110 allows.
func validHeaderName(s string) bool {
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case strings.ContainsRune("!#$%&'*+-.^_`|~", r):
		default:
			return false
		}
	}
	return s != ""
}

func editDialog(c *ui.Context, env *PageEnv, st *accountsState) {
	e := &st.edit
	if !e.open {
		return
	}
	t := c.Theme()
	fields, ferr := editFields(e)
	changed := ferr == nil && len(fields) > 1
	ui.Modal(c, &e.open, func() {
		ui.Column(c).Width(540).Padding(20).Gap(12).Children(func() {
			ui.Text(c, T("编辑账号", "Edit Account")).FontSize(17).FontWeight(600)
			ui.Text(c, fmt.Sprintf(T("%s · 将写入实例“%s”的凭证文件", "%s · written to the credential file on “%s”"), e.title, env.InstanceName())).FontSize(12).TextColor(t.TextMuted).MaxLines(2)
			ui.Scroll(c.Key("edit-scroll")).MaxHeight(dialogHeight(c, 430, 240)).Gap(12).Children(func() {
				ui.Form(c, func() {
					ui.Field(c, T("优先级", "Priority"), func() {
						ui.TextInput(c.Key("priority"), &e.priority).Placeholder(T("整数，留空表示不设置", "An integer; empty means unset")).AutoFocus()
					})
					ui.Field(c, T("备注", "Note"), func() {
						ui.TextInput(c.Key("note"), &e.note).Placeholder(T("可选", "Optional"))
					})
					ui.Field(c, T("前缀", "Prefix"), func() {
						ui.TextInput(c.Key("prefix"), &e.prefix).Placeholder(T("模型名前缀，可选", "A model name prefix; optional"))
					})
					ui.Field(c, T("代理地址", "Proxy URL"), func() {
						ui.TextInput(c.Key("proxy"), &e.proxy).Placeholder(T("例如 socks5://127.0.0.1:1080，可选", "e.g. socks5://127.0.0.1:1080; optional"))
					})
				})
				editHeaders(c, e)
				editLoadBar(c, env, st)
			})
			ui.Text(c, T("数值越大优先级越高。", "A higher number means a higher priority.")).FontSize(11).TextColor(t.TextMuted)
			if ferr != nil {
				ui.Text(c, ferr.Error()).FontSize(12).TextColor(t.Danger)
			}
			ErrorBanner(c, e.save.Err)
			if e.save.Running {
				Busy(c, T("正在保存…", "Saving…"))
			}
			ui.Row(c).Gap(8).Children(func() {
				ui.Box(c).Grow(1)
				if ui.Button(c.Key("cancel-edit"), T("取消", "Cancel")).Disabled(e.save.Running).Clicked() {
					e.open = false
				}
				if ui.PrimaryButton(c.Key("save-edit"), T("保存", "Save")).Disabled(e.save.Running || e.load.Running || ferr != nil || !changed || env.Client == nil).Clicked() {
					saveAccountEdit(env, st)
				}
			})
		})
	})
}

// editHeaders is the key/value rows of the custom headers.
func editHeaders(c *ui.Context, e *accountEdit) {
	t := c.Theme()
	ui.Field(c, T("自定义请求头", "Custom headers"), func() {
		ui.Column(c).Gap(6).Children(func() {
			remove := -1
			for i := range e.headers {
				r := &e.headers[i]
				ui.Row(c.Key(r.id)).Gap(8).AlignItems(ui.Center).Children(func() {
					ui.TextInput(c.Key("hk"), &r.key).Placeholder(T("名称，如 X-Org", "Name, e.g. X-Org")).Grow(1).Basis(0).Label(T("请求头名称", "Header name"))
					ui.TextInput(c.Key("hv"), &r.val).Placeholder(T("值，留空则删除", "Value; blank removes")).Grow(1).Basis(0).Label(T("请求头值", "Header value"))
					b := ui.ButtonBase(c.Key("hx")).Label(T("移除请求头", "Remove header")).Size(28, 28).Radius(999).Justify(ui.Center).AlignItems(ui.Center)
					bg := t.Text.Alpha(0.06)
					if b.Hovered() {
						bg = t.Text.Alpha(0.12)
					}
					b.Background(bg).Children(func() { ui.Icon(c, icX).FontSize(12).TextColor(t.TextMuted) })
					if b.Clicked() {
						remove = i
					}
				})
			}
			if remove >= 0 {
				e.headers = slices.Delete(e.headers, remove, remove+1)
			}
			ui.Row(c).Children(func() {
				if PillButton(c.Key("add-header"), icPlus, T("添加请求头", "Add Header"), false).Clicked() {
					e.addHeader()
				}
			})
			ui.Text(c, T("值留空并保存，会删除该请求头。", "Save a header with an empty value to remove it.")).FontSize(11).TextColor(t.TextMuted)
		})
	})
}

// editLoadBar offers to read the current prefix, proxy URL and headers:
// the account list does not carry them.
func editLoadBar(c *ui.Context, env *PageEnv, st *accountsState) {
	t := c.Theme()
	e := &st.edit
	if e.loaded {
		ui.Text(c, T("已读取服务器上的当前值；清空某一项并保存即可清除它。", "Showing the server's current values; empty a field and save to clear it.")).FontSize(11).TextColor(t.TextMuted)
		return
	}
	Card(c).Padding(10, 12).Gap(8).Background(pal(t).Inset).Children(func() {
		ui.Text(c, T("账号列表不含前缀、代理地址和请求头，所以这里先显示为空，留空表示不修改。点击“读取当前值”会下载此凭证文件，只取这三项，不保存其中的令牌。", "The account list does not include the prefix, proxy URL and headers, so they start empty, and empty means unchanged. “Read Current Values” downloads this credential file and keeps only those three fields, never its tokens.")).
			FontSize(11).TextColor(t.TextMuted)
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			if BusyPillButton(c.Key("load-fields"), icRefresh, T("读取当前值", "Read Current Values"), e.load.Running, env.Client == nil || e.save.Running).Clicked() {
				loadEditFields(env, st)
			}
			TaskStatus(c, &e.load, T("正在读取…", "Reading…"), "")
		})
	})
}

// loadEditFields reads the credential file for the editor's three fields.
// The file is read, not kept: only those fields leave this function.
func loadEditFields(env *PageEnv, st *accountsState) {
	e := &st.edit
	key, name, client := e.key, e.name, env.Client
	var f editableFields
	Run(env.Win, &e.load, 0, func(ctx context.Context) error {
		b, err := client.DownloadCredential(ctx, name)
		if err != nil {
			return err
		}
		f, _ = readEditableFields(b)
		return nil
	}, func(err error) {
		if err == nil && st.edit.open && st.edit.key == key {
			st.edit.applyLoaded(f)
		}
	})
}

func saveAccountEdit(env *PageEnv, st *accountsState) {
	e := &st.edit
	fields, err := editFields(e)
	if err != nil {
		e.save.Err = err
		return
	}
	svc, id, client := env.App.Svc, env.InstanceID, env.Client
	key := e.key
	Run(env.Win, &e.save, 0, func(ctx context.Context) error {
		err := client.PatchCredentialFields(ctx, fields)
		refreshAfterWrite(svc, id, err)
		return err
	}, func(err error) {
		if err == nil && st.edit.key == key {
			st.edit.open = false
		}
	})
}
