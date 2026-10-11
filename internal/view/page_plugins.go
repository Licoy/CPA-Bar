package view

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/licoy/cpa-bar/internal/cpa"
)

func init() {
	RegisterPage(Page{
		ID:       "plugins",
		Order:    30,
		Group:    GroupManage,
		Title:    func() string { return T("插件", "Plugins") },
		Instance: true,
		View:     pluginsPage,
	})
}

const (
	pluginsTabInstalled = iota
	pluginsTabStore
)

const (
	// pluginsStoreInline is how many store entries show as plain rows of the
	// page; more are a list that builds the rows in view.
	pluginsStoreInline = 40
)

// pluginsStoreEntry is what the page reads of an entry of the plugin
// store; the rest of the entry is ignored.
type pluginsStoreEntry struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	Description      string `json:"description"`
	Author           string `json:"author"`
	Version          string `json:"version"`
	Installed        bool   `json:"installed"`
	InstalledVersion string `json:"installed_version"`
	UpdateAvailable  bool   `json:"update_available"`
	AuthRequired     bool   `json:"auth_required"`
	AuthConfigured   bool   `json:"auth_configured"`
}

func (e pluginsStoreEntry) title() string {
	if strings.TrimSpace(e.Name) != "" {
		return e.Name
	}
	return e.ID
}

type pluginsState struct {
	tab int

	list    *cpa.PluginList
	load    Task
	started bool
	// unsupported is set when the server has no plugin API.
	unsupported bool

	store      *cpa.PluginStore
	entries    []pluginsStoreEntry
	storeErrs  []string
	storeLoad  Task
	storeStart bool
	storeList  ui.ListState

	toggles map[string]*Task
	op      Task
	opDone  string

	quota pluginsQuota

	removeID, removeName string
	removeOpen           bool
	installID, installAs string
	installUpdate        bool
	installOpen          bool
}

func pluginsPage(c *ui.Context, env *PageEnv) {
	t := c.Theme()
	st := State(env, "plugins", func() *pluginsState { return &pluginsState{toggles: map[string]*Task{}} })
	cl := env.Client

	if env.Snap.Server.SupportPlugin == "0" {
		st.unsupported = true
	}
	if cl != nil && !st.unsupported && !st.started && !st.load.Running {
		st.started = true
		pluginsLoad(env, st)
	}

	PageHeader(c, T("插件", "Plugins"),
		T("管理当前实例的 CPA 插件：启用、卸载，或从插件商店安装。", "Manage the CPA plugins of the selected instance: enable, uninstall, or install from the plugin store."), func() {
			if BusyPillButton(c, icRefresh, T("重新加载", "Reload"), st.load.Running || st.storeLoad.Running, cl == nil || st.unsupported).Clicked() {
				pluginsLoad(env, st)
				if st.storeStart {
					pluginsLoadStore(env, st)
				}
			}
		})

	if st.unsupported {
		EmptyState(c, T("此服务器不支持插件", "This server has no plugin support"),
			T("该 CPA 构建未启用插件功能（响应头 X-CPA-SUPPORT-PLUGIN 为 0，通常是未启用 CGO 的构建），或版本过旧。", "This CPA build has plugins off (X-CPA-SUPPORT-PLUGIN is 0, usually a build without CGO), or the version is too old."))
		return
	}

	ui.Row(c).Children(func() {
		PillTabs(c.Key("plugins-tab"), &st.tab, T("已安装", "Installed"), T("插件商店", "Store"))
	})
	if st.op.Running {
		Busy(c, T("正在执行…", "Working…"))
	}
	ErrorBanner(c, st.op.Err)
	if st.op.Done && st.opDone != "" {
		ui.Text(c, st.opDone).FontSize(12).TextColor(t.Success)
	}

	switch st.tab {
	case pluginsTabInstalled:
		pluginsInstalled(c, env, st)
	default:
		if !st.storeStart && cl != nil {
			st.storeStart = true
			pluginsLoadStore(env, st)
		}
		pluginsStoreTab(c, env, st)
	}
	pluginsDialogs(c, env, st)
	pluginsQuotaDialog(c, env, st)
}

func pluginsLoad(env *PageEnv, st *pluginsState) {
	cl := env.Client
	if cl == nil {
		return
	}
	var out *cpa.PluginList
	Run(env.Win, &st.load, 0, func(ctx context.Context) error {
		var err error
		out, err = cl.Plugins(ctx)
		return err
	}, func(err error) {
		switch {
		case err == nil:
			st.list = out
		case pluginsMissing(err):
			st.unsupported = true
		}
	})
}

func pluginsLoadStore(env *PageEnv, st *pluginsState) {
	cl := env.Client
	if cl == nil {
		return
	}
	var out *cpa.PluginStore
	Run(env.Win, &st.storeLoad, 0, func(ctx context.Context) error {
		var err error
		out, err = cl.PluginStore(ctx)
		return err
	}, func(err error) {
		if err != nil {
			return
		}
		st.store = out
		st.entries, st.storeErrs = pluginsParseStore(out)
	})
}

// pluginsMissing reports that the server answers no plugin routes.
func pluginsMissing(err error) bool {
	k := cpa.KindOf(err)
	return k == cpa.KindManagementUnavailable || k == cpa.KindNotFound
}

// pluginsParseStore reads the entries of the store defensively: an entry
// that is not an object with an id is skipped, and source errors are
// reduced to a line each.
func pluginsParseStore(s *cpa.PluginStore) (entries []pluginsStoreEntry, errs []string) {
	if s == nil {
		return nil, nil
	}
	for _, raw := range s.Plugins {
		var e pluginsStoreEntry
		if json.Unmarshal(raw, &e) != nil || strings.TrimSpace(e.ID) == "" {
			continue
		}
		entries = append(entries, e)
	}
	for _, raw := range s.SourceErrors {
		var e struct {
			Source  string `json:"source"`
			Name    string `json:"source_name"`
			ID      string `json:"source_id"`
			Message string `json:"message"`
			Error   string `json:"error"`
		}
		if json.Unmarshal(raw, &e) != nil {
			errs = append(errs, string(raw))
			continue
		}
		who := e.Name
		if who == "" {
			who = e.ID
		}
		if who == "" {
			who = e.Source
		}
		msg := e.Message
		if msg == "" {
			msg = e.Error
		}
		errs = append(errs, strings.TrimSpace(who+": "+msg))
	}
	return entries, errs
}

// pluginsMeta is the descriptive part of an installed plugin's entry,
// which cpa.Plugin keeps raw.
type pluginsMeta struct {
	Name        string
	Version     string
	Author      string
	Repo        string
	Description string
}

func pluginsMetaOf(p cpa.Plugin) pluginsMeta {
	var v struct {
		Metadata struct {
			Name    string `json:"name"`
			Version string `json:"version"`
			Author  string `json:"author"`
			Repo    string `json:"github_repository"`
			Desc    string `json:"description"`
		} `json:"metadata"`
	}
	_ = json.Unmarshal(p.Raw, &v)
	return pluginsMeta{Name: v.Metadata.Name, Version: v.Metadata.Version, Author: v.Metadata.Author, Repo: v.Metadata.Repo, Description: v.Metadata.Desc}
}

func pluginsInstalled(c *ui.Context, env *PageEnv, st *pluginsState) {
	t := c.Theme()
	if st.load.Running && st.list == nil {
		return
	}
	ErrorBanner(c, st.load.Err)
	if st.list == nil {
		return
	}
	if !st.list.PluginsEnabled {
		Notice(c, T("此实例的插件功能处于关闭状态（配置 plugins.enabled: false），已安装的插件不会运行。", "Plugins are switched off on this instance (plugins.enabled: false); installed plugins do not run."))
	}
	if st.list.PluginsDir != "" {
		ui.Text(c, T("插件目录：", "Plugins directory: ")+st.list.PluginsDir).FontSize(12).TextColor(t.TextMuted).Selectable().Padding(0, 4)
	}
	if len(st.list.Plugins) == 0 {
		EmptyState(c, T("尚未安装插件", "No plugins installed"), T("到“插件商店”查看可安装的插件。", "See the Store tab for plugins you can install."))
		return
	}
	Card(c).Padding(4, 0).Gap(0).Children(func() {
		for i, p := range st.list.Plugins {
			if i > 0 {
				logsRule(c)
			}
			meta := pluginsMetaOf(p)
			task := st.toggles[p.ID]
			if task == nil {
				task = &Task{}
				st.toggles[p.ID] = task
			}
			pluginRow(c, env, st, p, meta, task)
		}
	})
}

func pluginRow(c *ui.Context, env *PageEnv, st *pluginsState, p cpa.Plugin, meta pluginsMeta, task *Task) {
	t := c.Theme()
	cl := env.Client
	state, col, tint := T("已停用", "Off"), t.TextMuted, TintGray
	switch {
	case p.EffectiveEnabled:
		state, col, tint = T("运行中", "Running"), t.Success, TintGreen
	case p.Enabled:
		state, col, tint = T("未生效", "Not in effect"), t.Warning, TintOrange
	}
	ui.Row(c).Key(p.ID).Gap(12).Padding(12, 14).AlignItems(ui.Center).Children(func() {
		Tile(c, icPuzzle, tint, 38, false)
		ui.Column(c).Grow(1).Basis(0).Gap(2).Children(func() {
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Text(c, pluginsMetaName(meta, p.ID)).FontSize(13).FontWeight(500).SingleLine()
				if meta.Version != "" {
					ui.Text(c, "v"+strings.TrimPrefix(meta.Version, "v")).FontSize(12).TextColor(t.TextMuted).SingleLine()
				}
				if !p.Registered {
					Pill(c, T("未加载", "Not loaded"), t.Warning)
				}
			})
			if meta.Description != "" {
				ui.Text(c, meta.Description).FontSize(12).MaxLines(2).Selectable()
			}
			sub := "id: " + p.ID
			if meta.Author != "" {
				sub += " · " + meta.Author
			}
			if p.SupportsOAuth {
				sub += " · OAuth"
			}
			if p.SupportsQuota {
				sub += " · " + T("支持额度", "Quota")
			}
			ui.Text(c, sub).FontSize(12).TextColor(t.TextMuted).Selectable()
			if p.Path != "" {
				ui.Text(c, p.Path).FontSize(11).TextColor(t.TextMuted).SingleLine().Selectable()
			}
			if p.Enabled && !p.EffectiveEnabled {
				ui.Text(c, T("已启用，但尚未生效（插件功能关闭或未加载）", "Enabled, not in effect (plugins off or not loaded)")).FontSize(11).TextColor(t.Warning)
			}
			if task.Running {
				Busy(c, T("正在保存…", "Saving…"))
			}
			ErrorBanner(c, task.Err)
			if task.Done && c.Now().Sub(task.At) < 4*time.Second {
				done := fmt.Sprintf(T("已在“%[1]s”停用 %[2]s", "Disabled %[2]s on “%[1]s”"), env.InstanceName(), p.ID)
				if p.Enabled {
					done = fmt.Sprintf(T("已在“%[1]s”启用 %[2]s", "Enabled %[2]s on “%[1]s”"), env.InstanceName(), p.ID)
				}
				ui.Text(c, done).FontSize(11).TextColor(t.Success)
				c.After(4 * time.Second)
			}
		})
		Pill(c, state, col)
		if pluginHasQuota(p) {
			if PillButton(c, icGauge, T("查看额度", "View Quota"), cl == nil).Clicked() {
				pluginsOpenQuota(env, st, p, meta)
			}
		}
		on := p.Enabled
		sw := ui.Switch(c.Key("plugin-on-"+p.ID), &on).Label(fmt.Sprintf(T("启用 %s", "Enable %s"), p.ID)).Disabled(cl == nil || task.Running)
		if sw.Changed() {
			id, want := p.ID, on
			Run(env.Win, task, 0, func(ctx context.Context) error {
				return cl.SetPluginEnabled(ctx, id, want)
			}, func(err error) {
				pluginsLoad(env, st)
			})
		}
		if logsDangerPill(c, icTrash, T("卸载", "Uninstall"), cl == nil || st.op.Running).Clicked() {
			st.removeID, st.removeName, st.removeOpen = p.ID, pluginsMetaName(meta, p.ID), true
		}
	})
}

func pluginsMetaName(m pluginsMeta, id string) string {
	if m.Name != "" {
		return m.Name
	}
	return id
}

func pluginsStoreTab(c *ui.Context, env *PageEnv, st *pluginsState) {
	t := c.Theme()
	if st.storeLoad.Running && st.store == nil {
		Busy(c, T("正在读取插件商店（由服务器访问插件源）…", "Loading the store (the server contacts the plugin sources)…"))
		return
	}
	ErrorBanner(c, st.storeLoad.Err)
	if st.store == nil {
		return
	}
	for _, e := range st.storeErrs {
		ui.Text(c, T("插件源出错：", "Source error: ")+e).FontSize(12).TextColor(t.Warning).Selectable().Padding(0, 4)
	}
	Notice(c, fmt.Sprintf(T("安装会在实例“%s”的服务器上下载并运行第三方代码，插件能访问该服务器上的账号凭据与配置。只安装你信任的插件。", "Installing downloads and runs third-party code on the server of “%s”; a plugin can reach that server's credentials and configuration. Install only plugins you trust."), env.InstanceName()))
	if len(st.entries) == 0 {
		EmptyState(c, T("插件商店里没有可用插件", "The store has no plugins"), "")
		return
	}
	// A long store builds only the rows in view. Built whole, the rows of a
	// store of some hundred entries make more text layouts per frame than
	// the text system keeps (it holds 4096), and every frame, scrolling
	// included, shaped them all again.
	if len(st.entries) > pluginsStoreInline {
		st.storeList.Key = func(i int) any { return st.entries[i].ID }
		fillHeight(ui.Column(c), 320).Radius(14).Background(pal(t).Card).Clip().Children(func() {
			ui.List(c.Key("plugins-store-list"), &st.storeList, len(st.entries), func(i int) {
				ui.Column(c).Gap(0).Children(func() {
					if i > 0 {
						logsRule(c)
					}
					pluginsStoreRow(c, env, st, st.entries[i])
				})
			}).Grow(1)
		})
		return
	}
	Card(c).Padding(4, 0).Gap(0).Children(func() {
		for i, e := range st.entries {
			if i > 0 {
				logsRule(c)
			}
			pluginsStoreRow(c, env, st, e)
		}
	})
}

// pluginsStoreRow is one entry of the store.
func pluginsStoreRow(c *ui.Context, env *PageEnv, st *pluginsState, e pluginsStoreEntry) {
	t := c.Theme()
	cl := env.Client
	ui.Row(c).Key("store-"+e.ID).Gap(12).Padding(12, 14).AlignItems(ui.Center).Children(func() {
		tint := TintBlue
		if e.Installed {
			tint = TintGreen
		}
		Tile(c, icPuzzle, tint, 38, false)
		ui.Column(c).Grow(1).Basis(0).Gap(2).Children(func() {
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Text(c, e.title()).FontSize(13).FontWeight(500).SingleLine()
				if e.Version != "" {
					ui.Text(c, "v"+strings.TrimPrefix(e.Version, "v")).FontSize(12).TextColor(t.TextMuted).SingleLine()
				}
				if e.Installed {
					Pill(c, T("已安装", "Installed")+pick(e.InstalledVersion != "", " v"+strings.TrimPrefix(e.InstalledVersion, "v"), ""), t.Success)
				}
				if e.UpdateAvailable {
					Pill(c, T("有更新", "Update"), t.Warning)
				}
			})
			if e.Description != "" {
				ui.Text(c, e.Description).FontSize(12).MaxLines(3).Selectable()
			}
			sub := "id: " + e.ID
			if e.Author != "" {
				sub += " · " + e.Author
			}
			ui.Text(c, sub).FontSize(12).TextColor(t.TextMuted).Selectable()
			if e.AuthRequired && !e.AuthConfigured {
				ui.Text(c, T("此插件源需要认证，服务器上尚未配置。", "This source needs authentication, which is not configured on the server.")).FontSize(11).TextColor(t.Warning)
			}
		})
		label, icon := T("安装", "Install"), icPlus
		switch {
		case e.UpdateAvailable:
			label, icon = T("更新", "Update"), icRefresh
		case e.Installed:
			label, icon = T("重新安装", "Reinstall"), icRefresh
		}
		if PillButton(c, icon, label, cl == nil || st.op.Running).Clicked() {
			st.installID, st.installAs, st.installUpdate, st.installOpen = e.ID, e.title(), e.Installed, true
		}
	})
}

func pluginsDialogs(c *ui.Context, env *PageEnv, st *pluginsState) {
	cl := env.Client
	name := env.InstanceName()
	if st.removeOpen {
		msg := fmt.Sprintf(T("将从实例“%s”的服务器上卸载插件“%s”并删除其文件。已加载的插件可能需要重启服务器才能卸载。", "This uninstalls the plugin “%[2]s” from the server of “%[1]s” and deletes its files. A loaded plugin may need a server restart to be removed."), name, st.removeName)
		if ui.AlertDialog(c, &st.removeOpen, fmt.Sprintf(T("卸载“%s”？", "Uninstall “%s”?"), st.removeName), msg, T("取消", "Cancel"), T("确认卸载", "Confirm Uninstall")) == 1 && cl != nil {
			id, label := st.removeID, st.removeName
			st.opDone = ""
			Run(env.Win, &st.op, 0, func(ctx context.Context) error { return cl.DeletePlugin(ctx, id) }, func(err error) {
				if err == nil {
					st.opDone = fmt.Sprintf(T("已从“%s”卸载 %s", "Uninstalled %[2]s from “%[1]s”"), name, label)
					pluginsLoad(env, st)
					if st.storeStart {
						pluginsLoadStore(env, st)
					}
				}
			})
		}
	}
	if st.installOpen {
		verb := T("安装", "install")
		if st.installUpdate {
			verb = T("重新安装或更新", "reinstall or update")
		}
		msg := fmt.Sprintf(T("将在实例“%[1]s”的服务器上%[3]s插件“%[2]s”。插件是第三方代码，会以服务器进程的权限运行，可访问该服务器上的账号凭据与配置，安装后会自动启用。确定信任它吗？",
			"This will %[3]s the plugin “%[2]s” on the server of “%[1]s”. A plugin is third-party code that runs with the server's own permissions and can reach its credentials and configuration; it is enabled on install. Do you trust it?"), name, st.installAs, verb)
		if ui.AlertDialog(c, &st.installOpen, fmt.Sprintf(T("在“%s”上安装插件？", "Install a plugin on “%s”?"), name), msg, T("取消", "Cancel"), T("确认安装", "Confirm Install")) == 1 && cl != nil {
			id, label := st.installID, st.installAs
			st.opDone = ""
			Run(env.Win, &st.op, 0, func(ctx context.Context) error { return cl.InstallPlugin(ctx, id) }, func(err error) {
				if err == nil {
					st.opDone = fmt.Sprintf(T("已在“%s”安装 %s", "Installed %[2]s on “%[1]s”"), name, label)
					pluginsLoad(env, st)
					pluginsLoadStore(env, st)
				}
			})
		}
	}
}
