package view

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/licoy/cpa-bar/internal/core"
	"github.com/licoy/cpa-bar/internal/cpa"
)

func init() {
	RegisterPage(Page{
		ID:       "accounts",
		Order:    0,
		Group:    GroupManage,
		Title:    func() string { return T("账号", "Accounts") },
		Instance: true,
		View:     accountsPage,
	})
}

// maxCredentialFile bounds a file read for import: credential files are a
// few kilobytes, anything larger is not one.
const maxCredentialFile = 4 << 20

const (
	// accountsListLeast is the least height of the list, which otherwise
	// takes the height the window leaves.
	accountsListLeast = 360
	// accountsRowEstimate is about the height of a row, so that a short
	// list does not keep empty space under it.
	accountsRowEstimate = 96
)

// Health filters of the list, in the order of the segmented control.
const (
	filterAll = iota
	filterAvailable
	filterError
	filterCooling
	filterDisabled
)

type accountsState struct {
	search   string
	provider string
	health   int
	list     ui.ListState

	// rows keeps the task of each account, by credKeyOf, so that a
	// spinner or an error stays on the account it belongs to.
	rows map[string]*Task
	// op is the page-level write: import, refresh all, bulk delete.
	op Task

	importShown bool
	importOK    int
	importLines []string

	selected       map[string]bool
	refreshAllOpen bool

	del    deleteAsk
	models modelsView
	edit   accountEdit
}

func newAccountsState() *accountsState {
	return &accountsState{rows: map[string]*Task{}, selected: map[string]bool{}, models: modelsView{entries: map[string]*modelsEntry{}}}
}

func (s *accountsState) rowTask(key string) *Task {
	t := s.rows[key]
	if t == nil {
		t = &Task{}
		s.rows[key] = t
	}
	return t
}

// deleteAsk is the confirmation of a delete.
type deleteAsk struct {
	open   bool
	keys   []string
	names  []string // server file names
	labels []string // what the user knows them by
}

type modelsEntry struct {
	task Task
	list []cpa.CredentialModel
	ls   ui.ListState
}

type modelsView struct {
	open    bool
	key     string
	title   string
	entries map[string]*modelsEntry
}

func accountsPage(c *ui.Context, env *PageEnv) {
	t := c.Theme()
	a := env.App
	snap := env.Snap
	st := State(env, "accounts", newAccountsState)

	canWrite := env.Client != nil
	// Writes that depend on the current state wait for fresh data.
	fresh := canWrite && snap.HasData && !snap.Stale
	allProviders := T("全部服务商", "All providers")
	if st.provider == "" {
		st.provider = allProviders
	}

	sub := fmt.Sprintf(T("目标实例：%s", "Target instance: %s"), env.InstanceName())
	if snap.HasData {
		sub += " · " + fmt.Sprintf(T("共 %d 个账号", "%d accounts"), len(snap.Credentials))
	}
	PageHeader(c, T("账号", "Accounts"), sub, func() {
		if PillButton(c.Key("import"), icPlus, T("导入", "Import"), !canWrite || st.op.Running).
			Tooltip(fmt.Sprintf(T("上传凭证 JSON 文件到“%s”", "Upload credential JSON files to “%s”"), env.InstanceName())).Clicked() {
			importAccounts(env, st)
		}
		if PillButton(c.Key("refresh-all"), icKey, T("刷新全部 OAuth", "Refresh All OAuth"), !canWrite || st.op.Running).
			Tooltip(T("让服务器刷新所有 OAuth 账号的令牌", "Ask the server to refresh the tokens of all OAuth accounts")).Clicked() {
			st.refreshAllOpen = true
		}
		if BusyPillButton(c.Key("refresh-now"), icRefresh, T("立即刷新", "Refresh Now"), instanceBusy(snap), snap.Profile.Paused).Clicked() {
			a.Svc.Refresh(env.InstanceID)
		}
	})

	ErrorBanner(c, st.op.Err)
	if st.op.Running {
		Busy(c, T("正在处理…", "Working…"))
	}
	accountsImportReport(c, st)
	if snap.Stale {
		Notice(c, fmt.Sprintf(T("最近一次刷新失败，以下数据来自 %s，可能已过期；启停和删除暂不可用。", "The last refresh failed; the data below is from %s and may be out of date. Enabling, disabling and deleting wait for fresh data."), Ago(snap.LastSuccess, env.Now)))
	}
	if !snap.HasData {
		if snap.Err != nil {
			ErrorBanner(c, snap.Err)
		} else if canWrite {
			Busy(c, T("正在读取账号…", "Reading accounts…"))
		}
		return
	}

	acc := snap.Accounts
	num := func(n int) string { return staleNumIf(n, snap) }
	ui.Row(c).Gap(10).Children(func() {
		Stat(c, num(acc.Available), T("可用", "Available"), t.Success)
		Stat(c, num(acc.Cooling), T("冷却中", "Cooling"), pick(acc.Cooling > 0, t.Warning, t.Text))
		Stat(c, num(acc.Error), T("异常", "Error"), pick(acc.Error > 0, t.Danger, t.Text))
		Stat(c, num(acc.Disabled), T("已停用", "Disabled"), t.Text)
		Stat(c, num(acc.Total), T("共", "Total"), t.Text)
	})

	// Filters.
	// The filter lists the providers by name and filters by their ids.
	providers := distinctProviders(snap.Credentials)
	options := []string{allProviders}
	for _, p := range providers {
		options = append(options, providerLabel(p))
	}
	if slices.Contains(providers, st.provider) {
		st.provider = providerLabel(st.provider)
	}
	if !slices.Contains(options, st.provider) {
		st.provider = allProviders
	}
	ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
		ui.TextInput(c.Key("search"), &st.search).Placeholder(T("搜索名称、邮箱、备注、服务商、状态…", "Search name, email, label, provider, status…")).Grow(1).Label(T("搜索账号", "Search accounts"))
		ui.Select(c.Key("provider"), &st.provider, options).Width(170).Label(T("服务商筛选", "Provider filter"))
		PillTabs(c.Key("health"), &st.health, T("全部", "All"), T("可用", "Available"), T("异常", "Error"), T("冷却", "Cooling"), T("停用", "Disabled"))
	})

	provider := ""
	for _, p := range providers {
		if st.provider == providerLabel(p) {
			provider = p
		}
	}
	rows := sortedForPanel(filterCredentials(snap.Credentials, st.search, provider, st.health))
	st.pruneSelection(snap.Credentials)

	// Selection and bulk delete.
	deletable := 0
	allSelected := true
	for _, cr := range rows {
		if cr.RuntimeOnly {
			continue
		}
		deletable++
		if !st.selected[credKeyOf(cr)] {
			allSelected = false
		}
	}
	if deletable > 0 {
		ui.Row(c).Gap(10).Padding(0, 4).AlignItems(ui.Center).Children(func() {
			all := allSelected
			if ui.Checkbox(c.Key("select-all"), &all, T("选择当前列表", "Select listed")).Changed() {
				for _, cr := range rows {
					if !cr.RuntimeOnly {
						st.selected[credKeyOf(cr)] = all
					}
				}
			}
			n := st.selectedCount()
			if n > 0 {
				ui.Text(c, fmt.Sprintf(T("已选 %d 个", "%d selected"), n)).FontSize(12).TextColor(t.TextMuted)
				if PillButton(c.Key("clear-selection"), nil, T("清除选择", "Clear"), false).Clicked() {
					clear(st.selected)
				}
				if ui.Button(c.Key("delete-selected"), fmt.Sprintf(T("删除所选 (%d)", "Delete Selected (%d)"), n)).TextColor(t.Danger).Radius(999).
					Disabled(!fresh || st.op.Running).Clicked() {
					st.askDelete(snap.Credentials, nil)
				}
			}
		})
	}

	switch {
	case len(snap.Credentials) == 0:
		EmptyState(c, T("这个实例还没有账号", "This instance has no accounts yet"),
			T("可在“OAuth 登录”页登录，或点击“导入”上传凭证文件。", "Log in on the OAuth Login page, or click Import to upload credential files."))
	case len(rows) == 0:
		EmptyState(c, T("没有符合条件的账号", "No accounts match"), T("请调整搜索或筛选条件。", "Adjust the search or the filters."))
	default:
		st.list.Key = func(i int) any { return credKeyOf(rows[i]) }
		// The list takes the height the window leaves; a short list keeps
		// to about its rows.
		fillHeight(ui.List(c.Key("list"), &st.list, len(rows), func(i int) {
			accountRow(c, env, st, rows[i], canWrite, fresh)
		}), min(accountsListLeast, float32(len(rows))*accountsRowEstimate))
	}

	accountDialogs(c, env, st, fresh)
}

// accountsImportReport shows what an import did, until dismissed.
func accountsImportReport(c *ui.Context, st *accountsState) {
	if !st.importShown {
		return
	}
	t := c.Theme()
	Card(c).Children(func() {
		if st.importOK > 0 {
			ui.Text(c, fmt.Sprintf(T("已导入 %d 个文件。", "Imported %d files."), st.importOK)).FontSize(12).TextColor(t.Success)
		}
		if len(st.importLines) > 0 {
			ui.Text(c, fmt.Sprintf(T("%d 个文件未导入：", "%d files were not imported:"), len(st.importLines))).FontSize(12).FontWeight(600).TextColor(t.Danger)
			for i, l := range st.importLines {
				ui.Text(c.Key(i), l).FontSize(12).Selectable().MaxLines(3)
			}
		}
		ui.Row(c).Children(func() {
			if PillButton(c.Key("dismiss-import"), nil, T("关闭", "Dismiss"), false).Clicked() {
				st.importShown = false
			}
		})
	})
}

// accountRow is one account: a soft card with its health, who it is, its
// counts, status, the switch, Edit, and the rest of the actions in a menu.
func accountRow(c *ui.Context, env *PageEnv, st *accountsState, cr cpa.Credential, canWrite, fresh bool) {
	t := c.Theme()
	key := credKeyOf(cr)
	h := cr.Health()
	task := st.rowTask(key)
	busy := task.Running
	instance := env.InstanceName()
	col := HealthColor(t, h)

	ui.Column(c).Padding(0, 0, 8, 0).Children(func() {
		Card(c).Padding(10, 14).Gap(6).Children(func() {
			ui.Row(c).Gap(12).AlignItems(ui.Center).Children(func() {
				if !cr.RuntimeOnly {
					sel := st.selected[key]
					if ui.Checkbox(c.Key("sel"), &sel, "").Label(T("选择账号", "Select account")).Changed() {
						st.selected[key] = sel
					}
				}
				credTile(c.Key("tile"), cr, 34, pal(t).Card)
				ui.Column(c).Grow(1).Basis(0).Gap(2).Children(func() {
					ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
						ui.Text(c, PrivName(cr.DisplayName())).FontSize(13).FontWeight(500).SingleLine()
						if cr.AccountType != "" {
							Pill(c.Key("type"), cr.AccountType, TintGray.Text(t))
						}
						if cr.RuntimeOnly {
							Pill(c.Key("mem"), T("仅内存", "Memory only"), TintIndigo.Text(t)).
								Tooltip(T("仅存在于服务器内存，不能在这里启停、删除或下载", "Exists only in server memory; it cannot be toggled, deleted or downloaded here"))
						}
					})
					if sub := accountSubline(cr, h, env.Now); sub != "" {
						ui.Text(c, sub).FontSize(11).TextColor(t.TextMuted).SingleLine()
					}
					if d := accountDetail(cr, h, env.Now); d != "" {
						ui.Text(c, d).FontSize(11).TextColor(t.TextMuted).MaxLines(2)
					}
					if u := env.App.usageAccountLine(env.InstanceID, cr.AuthIndex, env.Now); u != "" {
						ui.Text(c, u).FontSize(11).TextColor(t.TextMuted).SingleLine()
					}
					if cr.StatusMessage != "" {
						ui.Text(c, PrivText(cr.StatusMessage)).FontSize(11).TextColor(col).MaxLines(3).Selectable()
					}
				})
				ui.Text(c, fmt.Sprintf(T("成功 %d · 失败 %d", "OK %d · Failed %d"), cr.Success, cr.Failed)).FontSize(12).TextColor(t.TextMuted).FontFeatures("tnum").SingleLine()
				Pill(c.Key("status"), HealthLabel(h), col)

				on := h != cpa.HealthDisabled
				canToggle := fresh && !cr.RuntimeOnly && !busy
				tip := fmt.Sprintf(T("在“%s”上启用或停用此账号", "Enable or disable this account on “%s”"), instance)
				switch {
				case cr.RuntimeOnly:
					tip = T("仅内存账号不能在这里启停", "Memory-only accounts cannot be toggled here")
				case !fresh:
					tip = T("数据已过期，刷新成功后才能启停", "The data is out of date; refresh first")
				}
				if ui.Switch(c.Key("enabled"), &on).Disabled(!canToggle).Tooltip(tip).Label(T("启用", "Enabled")).Changed() {
					toggleAccount(env, task, cr, !on)
				}

				if PillButton(c.Key("edit"), nil, T("编辑", "Edit"), !canWrite || busy || cr.RuntimeOnly).Clicked() {
					st.edit = newAccountEdit(cr)
				}
				accountMenu(c, env, st, cr, task, canWrite, fresh)
			})
			env.App.quotaView(c, env.Snap, cr, env.Now, 3)
			TaskStatus(c, task, T("正在处理…", "Working…"), T("完成", "Done"))
		})
	})
}

// accountMenu is the "More" menu of a row: the actions used less often.
func accountMenu(c *ui.Context, env *PageEnv, st *accountsState, cr cpa.Credential, task *Task, canWrite, fresh bool) {
	h := cr.Health()
	busy := task.Running
	instance := env.InstanceName()
	moreButton(c, T("更多", "More"), func(m *ui.Menu) {
		if m.Item(T("刷新 OAuth", "Refresh OAuth")).Disabled(!canWrite || busy).Chosen() {
			refreshAccount(env, task, cr)
		}
		cooling := h == cpa.HealthCooling || h == cpa.HealthError
		if m.Item(T("重置冷却", "Reset Cooldown")).Disabled(!canWrite || busy || !cooling || cr.AuthIndex == "").Chosen() {
			resetCooldown(env, task, cr)
		}
		if m.Item(T("模型", "Models")).Disabled(!canWrite).Chosen() {
			openModels(env, st, cr)
		}
		if m.Item(T("下载", "Download")).Disabled(!canWrite || busy || cr.RuntimeOnly).Chosen() {
			downloadAccount(env, task, cr)
		}
		m.Separator()
		if m.Item(T("删除…", "Delete…")).Disabled(!fresh || busy || cr.RuntimeOnly).Chosen() {
			st.askDelete([]cpa.Credential{cr}, []string{credKeyOf(cr)})
		}
	}).Tooltip(fmt.Sprintf(T("“%s”上此账号的更多操作", "More actions for this account on “%s”"), instance))
}

// accountDialogs are the confirmations and editors of the page.
func accountDialogs(c *ui.Context, env *PageEnv, st *accountsState, fresh bool) {
	instance := env.InstanceName()

	if st.refreshAllOpen {
		msg := fmt.Sprintf(T("将让实例“%s”刷新所有 OAuth 账号的令牌。这会向各服务商发起请求。", "This asks instance “%s” to refresh the tokens of all its OAuth accounts. It contacts each provider."), instance)
		if ui.AlertDialog(c, &st.refreshAllOpen, T("刷新全部 OAuth？", "Refresh all OAuth?"), msg, T("取消", "Cancel"), T("确认刷新", "Confirm Refresh")) == 1 {
			refreshAllAccounts(env, st)
		}
	}

	if st.del.open {
		labels := st.del.labels
		shown := labels
		if len(shown) > 8 {
			shown = shown[:8]
		}
		list := strings.Join(shown, "\n")
		if len(labels) > len(shown) {
			list += "\n" + fmt.Sprintf(T("…以及另外 %d 个", "…and %d more"), len(labels)-len(shown))
		}
		msg := fmt.Sprintf(T("将从实例“%s”的服务器上永久删除 %d 个账号的凭证文件，无法撤销：\n%s", "This permanently deletes the credential files of %[2]d accounts from instance “%[1]s”, and cannot be undone:\n%[3]s"), instance, len(labels), list)
		if ui.AlertDialog(c, &st.del.open, T("删除账号？", "Delete accounts?"), msg, T("取消", "Cancel"), T("确认删除", "Confirm Delete")) == 1 && fresh {
			deleteAccounts(env, st, st.del)
		}
	}

	modelsDialog(c, env, st)
	editDialog(c, env, st)
}

func modelsDialog(c *ui.Context, env *PageEnv, st *accountsState) {
	if !st.models.open {
		return
	}
	t := c.Theme()
	entry := st.models.entries[st.models.key]
	ui.Modal(c, &st.models.open, func() {
		ui.Column(c).Width(600).Padding(20).Gap(10).Children(func() {
			ui.Text(c, T("账号模型", "Account Models")).FontSize(17).FontWeight(600)
			ui.Text(c, fmt.Sprintf("%s · %s", st.models.title, env.InstanceName())).FontSize(12).TextColor(t.TextMuted).SingleLine()
			if entry == nil {
				return
			}
			TaskStatus(c, &entry.task, T("正在读取模型…", "Reading models…"), "")
			if entry.task.Done && len(entry.list) == 0 {
				ui.Text(c, T("此账号没有可用模型。", "This account serves no models.")).FontSize(12).TextColor(t.TextMuted)
			}
			if len(entry.list) > 0 {
				ui.Text(c, fmt.Sprintf(T("共 %d 个模型", "%d models"), len(entry.list))).FontSize(12).TextColor(t.TextMuted)
				ui.List(c.Key("models"), &entry.ls, len(entry.list), func(i int) {
					m := entry.list[i]
					ui.Row(c).Gap(10).Padding(6, 12).AlignItems(ui.Center).Children(func() {
						ui.Text(c, m.ID).Font("monospace").FontSize(12).Grow(1).Basis(0).SingleLine().Selectable()
						ui.Text(c, m.DisplayName).FontSize(12).Width(150).SingleLine().TextColor(t.TextMuted)
						ui.Text(c, m.OwnedBy).FontSize(12).Width(110).SingleLine().TextColor(t.TextMuted)
					})
				}).MaxHeight(dialogHeight(c, 340, 240)).Radius(12).Background(pal(t).Card)
			}
			ui.Row(c).Children(func() {
				ui.Box(c).Grow(1)
				if ui.Button(c.Key("close-models"), T("关闭", "Close")).Clicked() {
					st.models.open = false
				}
			})
		})
	})
}

// ---- Operations ----
//
// Each captures what it needs when clicked: a result may arrive after the
// frame (and the window's selection) it started from, and lands in this
// instance's own state.

// refreshAfterWrite re-reads the instance after a write whose outcome may
// have changed it, so the user sees what happened without sending the
// write again.
func refreshAfterWrite(svc *core.Service, id string, err error) {
	switch cpa.KindOf(err) {
	case cpa.KindIndeterminate, cpa.KindConflict:
		svc.Refresh(id)
	default:
		if err == nil {
			svc.Refresh(id)
		}
	}
}

func toggleAccount(env *PageEnv, task *Task, cr cpa.Credential, disabled bool) {
	svc, id := env.App.Svc, env.InstanceID
	Run(env.Win, task, 0, func(ctx context.Context) error {
		return svc.SetCredentialDisabled(ctx, id, cr, disabled)
	}, nil)
}

func refreshAccount(env *PageEnv, task *Task, cr cpa.Credential) {
	svc, id, client := env.App.Svc, env.InstanceID, env.Client
	name := credFileName(cr)
	Run(env.Win, task, 0, func(ctx context.Context) error {
		err := client.RefreshCredential(ctx, name, cr.AuthIndex)
		refreshAfterWrite(svc, id, err)
		return err
	}, nil)
}

func refreshAllAccounts(env *PageEnv, st *accountsState) {
	svc, id, client := env.App.Svc, env.InstanceID, env.Client
	Run(env.Win, &st.op, 0, func(ctx context.Context) error {
		err := client.RefreshCredential(ctx, "", "")
		refreshAfterWrite(svc, id, err)
		return err
	}, nil)
}

func resetCooldown(env *PageEnv, task *Task, cr cpa.Credential) {
	svc, id, client := env.App.Svc, env.InstanceID, env.Client
	Run(env.Win, task, 0, func(ctx context.Context) error {
		err := client.ResetCooldown(ctx, cr.AuthIndex)
		refreshAfterWrite(svc, id, err)
		return err
	}, nil)
}

func openModels(env *PageEnv, st *accountsState, cr cpa.Credential) {
	key := credKeyOf(cr)
	entry := st.models.entries[key]
	if entry == nil {
		entry = &modelsEntry{}
		st.models.entries[key] = entry
	}
	st.models.open, st.models.key, st.models.title = true, key, PrivName(cr.DisplayName())
	client, name := env.Client, credFileName(cr)
	var list []cpa.CredentialModel
	Run(env.Win, &entry.task, 0, func(ctx context.Context) error {
		var err error
		list, err = client.CredentialModels(ctx, name)
		return err
	}, func(err error) {
		if err == nil {
			entry.list = list
		}
	})
}

// parsePriority reads the priority field: empty is unset.
func parsePriority(s string) (*int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return nil, err
	}
	return &n, nil
}

func priorityText(p *float64) string {
	if p == nil {
		return ""
	}
	return strconv.FormatFloat(*p, 'f', -1, 64)
}

func downloadAccount(env *PageEnv, task *Task, cr cpa.Credential) {
	name := credFileName(cr)
	client, win := env.Client, env.Win
	SaveFile(win, T("下载凭证文件", "Download Credential File"), filepath.Base(name), jsonFilter, func(path string) {
		Run(win, task, 0, func(ctx context.Context) error {
			b, err := client.DownloadCredential(ctx, name)
			if err != nil {
				return err
			}
			return writePrivateFile(path, b)
		}, nil)
	})
}

// writePrivateFile writes a file only its owner can read: a credential
// file holds tokens.
func writePrivateFile(path string, data []byte) error {
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return err
	}
	// WriteFile keeps the mode of a file that already existed.
	return os.Chmod(path, 0o600)
}

func importAccounts(env *PageEnv, st *accountsState) {
	win := env.Win
	title := T("导入凭证文件", "Import Credential Files")
	go func() {
		paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{Parent: liveWindow(win), Title: title, Filters: jsonFilter, Multiple: true})
		if err != nil || len(paths) == 0 {
			return
		}
		update(win, func() { uploadAccounts(env, st, paths) })
	}()
}

func uploadAccounts(env *PageEnv, st *accountsState, paths []string) {
	svc, id, client := env.App.Svc, env.InstanceID, env.Client
	var lines []string
	ok := 0
	Run(env.Win, &st.op, 0, func(ctx context.Context) error {
		for _, p := range paths {
			base := filepath.Base(p)
			data, err := readCredentialFile(p)
			if err == nil {
				err = client.UploadCredential(ctx, base, data)
			}
			if err != nil {
				lines = append(lines, base+": "+ErrorText(err))
				continue
			}
			ok++
		}
		svc.Refresh(id)
		return nil
	}, func(error) {
		st.importShown, st.importOK, st.importLines = true, ok, lines
	})
}

func readCredentialFile(path string) ([]byte, error) {
	if !strings.EqualFold(filepath.Ext(path), ".json") {
		return nil, fmt.Errorf("%s", T("只能导入 .json 文件", "Only .json files can be imported"))
	}
	fi, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if fi.Size() > maxCredentialFile {
		return nil, fmt.Errorf("%s", T("文件过大，不像凭证文件", "The file is too large to be a credential file"))
	}
	return os.ReadFile(path)
}

// askDelete opens the delete confirmation for the credentials with keys
// (all selected ones when keys is nil).
func (s *accountsState) askDelete(creds []cpa.Credential, keys []string) {
	ask := deleteAsk{open: true}
	for _, cr := range creds {
		k := credKeyOf(cr)
		if cr.RuntimeOnly || (keys == nil && !s.selected[k]) {
			continue
		}
		ask.keys = append(ask.keys, k)
		ask.names = append(ask.names, credFileName(cr))
		label := PrivName(cr.DisplayName())
		if n := credFileName(cr); n != cr.DisplayName() {
			label += " (" + PrivName(n) + ")"
		}
		ask.labels = append(ask.labels, label)
	}
	if len(ask.names) > 0 {
		s.del = ask
	}
}

func deleteAccounts(env *PageEnv, st *accountsState, ask deleteAsk) {
	svc, id, client := env.App.Svc, env.InstanceID, env.Client
	task := &st.op
	if len(ask.keys) == 1 {
		task = st.rowTask(ask.keys[0])
	}
	Run(env.Win, task, 0, func(ctx context.Context) error {
		err := client.DeleteCredentials(ctx, ask.names...)
		refreshAfterWrite(svc, id, err)
		return err
	}, func(err error) {
		if err == nil {
			for _, k := range ask.keys {
				delete(st.selected, k)
			}
		}
	})
}

// ---- Selection and filters ----

func (s *accountsState) selectedCount() int {
	n := 0
	for _, v := range s.selected {
		if v {
			n++
		}
	}
	return n
}

// pruneSelection forgets accounts that are gone.
func (s *accountsState) pruneSelection(creds []cpa.Credential) {
	if len(s.selected) == 0 {
		return
	}
	live := make(map[string]bool, len(creds))
	for _, cr := range creds {
		live[credKeyOf(cr)] = !cr.RuntimeOnly
	}
	for k := range s.selected {
		if !live[k] {
			delete(s.selected, k)
		}
	}
}

// credFileName is the name the server knows a credential by.
func credFileName(cr cpa.Credential) string {
	if cr.Name != "" {
		return cr.Name
	}
	return cr.ID
}

func distinctProviders(creds []cpa.Credential) []string {
	var out []string
	for _, cr := range creds {
		if p := providerKey(cr.ProviderName()); p != "" && !slices.Contains(out, p) {
			out = append(out, p)
		}
	}
	slices.Sort(out)
	return out
}

// filterCredentials applies the search, the provider ("" for all) and the
// health filter.
func filterCredentials(creds []cpa.Credential, query, provider string, health int) []cpa.Credential {
	query = strings.ToLower(strings.TrimSpace(query))
	var out []cpa.Credential
	for _, cr := range creds {
		if provider != "" && providerKey(cr.ProviderName()) != providerKey(provider) {
			continue
		}
		if !healthMatches(cr.Health(), health) {
			continue
		}
		if query != "" && !credentialMatches(cr, query) {
			continue
		}
		out = append(out, cr)
	}
	return out
}

func healthMatches(h cpa.Health, filter int) bool {
	switch filter {
	case filterAvailable:
		return h == cpa.HealthAvailable
	case filterError:
		return h == cpa.HealthError
	case filterCooling:
		return h == cpa.HealthCooling
	case filterDisabled:
		return h == cpa.HealthDisabled
	}
	return true
}

// credentialMatches reports whether any searchable field has the lower-case
// query.
func credentialMatches(cr cpa.Credential, query string) bool {
	for _, f := range []string{cr.Name, cr.Email, cr.Label, cr.ProviderName(), providerLabel(cr.ProviderName()), cr.Account, cr.StatusMessage, cr.Note, cr.ID} {
		if strings.Contains(strings.ToLower(f), query) {
			return true
		}
	}
	return false
}

// accountSubline is the second line of a row: the provider, who the account
// is, and when a cooling one recovers.
func accountSubline(cr cpa.Credential, h cpa.Health, now time.Time) string {
	var parts []string
	if p := providerLabel(cr.ProviderName()); p != "" {
		parts = append(parts, p)
	}
	disp := cr.DisplayName()
	for _, s := range []string{cr.Email, cr.Account, cr.Name} {
		if s != "" && s != disp && !slices.Contains(parts, PrivName(s)) {
			parts = append(parts, PrivName(s))
		}
	}
	if h == cpa.HealthCooling && cr.NextRetryAfter != nil {
		parts = append(parts, fmt.Sprintf(T("%s恢复", "recovers %s"), Until(*cr.NextRetryAfter, now)))
	}
	return strings.Join(parts, " · ")
}

// accountDetail is the line of times and metadata under a row.
func accountDetail(cr cpa.Credential, h cpa.Health, now time.Time) string {
	var parts []string
	if cr.LastRefresh != nil {
		parts = append(parts, fmt.Sprintf(T("上次刷新 %s", "Refreshed %s"), Ago(*cr.LastRefresh, now)))
	}
	if cr.Priority != nil {
		parts = append(parts, fmt.Sprintf(T("优先级 %s", "priority %s"), priorityText(cr.Priority)))
	}
	if cr.Note != "" {
		parts = append(parts, fmt.Sprintf(T("备注：%s", "note: %s"), cr.Note))
	}
	return strings.Join(parts, " · ")
}
