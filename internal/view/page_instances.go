package view

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/licoy/cpa-bar/internal/core"
	"github.com/licoy/cpa-bar/internal/cpa"
	"github.com/licoy/cpa-bar/internal/cpamp"
	"github.com/licoy/cpa-bar/internal/store"
)

func init() {
	RegisterPage(Page{
		ID:    "instances",
		Order: 0,
		Group: GroupApp,
		Title: func() string { return T("实例", "Instances") },
		View:  instancesPage,
	})
}

// instanceEditor is the add or edit dialog.
type instanceEditor struct {
	open bool
	// id is the edited instance, "" when adding.
	id                    string
	name                  string
	address               string
	key                   string
	ca                    string
	icon                  string
	color                 string
	advanced              bool
	forwardAuthOnRedirect bool
	// flavor is the kind of server: flavorAuto has it detected on test
	// or save.
	flavor     int
	managerURL string
	managerKey string
	// notice says what detection found.
	notice string
	test   Task
	save   Task
}

const (
	flavorAuto = iota
	flavorOfficial
	flavorPlus
)

func (e *instanceEditor) input() core.InstanceInput {
	in := core.InstanceInput{Name: e.name, Address: e.address, Key: strings.TrimSpace(e.key), CACertPEM: e.ca, ForwardAuthOnRedirect: e.forwardAuthOnRedirect, Icon: e.icon, Color: e.color}
	if e.flavor == flavorPlus {
		in.Flavor, in.ManagerURL, in.ManagerKey = store.FlavorManagerPlus, e.managerURL, strings.TrimSpace(e.managerKey)
	}
	return in
}

// editorFor is the editor of an instance, or of a new one from a profile
// that has no ID (an imported one).
func editorFor(p store.InstanceProfile) instanceEditor {
	e := instanceEditor{open: true, id: p.ID, name: p.Name, ca: p.CACertPEM, forwardAuthOnRedirect: p.ForwardAuthOnRedirect, icon: p.Icon, color: p.Color, advanced: p.CACertPEM != "" || p.ForwardAuthOnRedirect, flavor: flavorOfficial}
	if p.BaseURL != "" {
		e.address = cpa.RootURL(p.BaseURL)
	}
	if p.ManagerPlus() {
		e.flavor, e.managerURL = flavorPlus, p.ManagerURL
	}
	return e
}

// detected stops a save or a test of an editor in flavorAuto when the
// address turns out to be CPA-Manager-Plus, or to have it beside: the
// editor needs more from the user.
type detected struct {
	det core.Detection
	// cpaAddress is the CPA Manager-Plus collects from, when it told.
	cpaAddress string
}

func (*detected) Error() string { return "CPA-Manager-Plus detected" }

// detect runs detection for an editor in flavorAuto: nil means plain CPA,
// to save as is.
func detect(ctx context.Context, svc *core.Service, in core.InstanceInput) error {
	det, err := svc.Detect(ctx, in.Address, in.CACertPEM)
	if err != nil || (!det.ManagerPlus && det.ManagerURL == "") {
		return err
	}
	d := &detected{det: det}
	if k := strings.TrimSpace(in.Key); det.ManagerPlus && strings.HasPrefix(k, "cpamp_") {
		d.cpaAddress, _ = svc.ManagerCPAAddress(ctx, cpamp.Options{BaseURL: in.Address, AdminKey: k, CACertPEM: []byte(in.CACertPEM), ForwardAuthOnRedirect: in.ForwardAuthOnRedirect})
	}
	return d
}

// applyDetection fills the editor with what detection found, on the main
// thread, and clears the task's error.
func (e *instanceEditor) applyDetection(t *Task, err error) {
	var d *detected
	if !errors.As(err, &d) {
		return
	}
	t.Err = nil
	e.flavor = flavorPlus
	if !d.det.ManagerPlus {
		e.managerURL = d.det.ManagerURL
		e.notice = fmt.Sprintf(T("检测到同一主机上的 CPA-Manager-Plus（%s）。填写它的管理员密钥即可从它读取用量；不需要的话请选择“官方 CPA”再保存。",
			"Found CPA-Manager-Plus on the same host (%s). Enter its admin key to read the usage from it, or choose Official CPA and save."), PrivURL(e.managerURL))
		return
	}
	e.managerURL = e.address
	if k := strings.TrimSpace(e.key); strings.HasPrefix(k, "cpamp_") {
		e.managerKey, e.key = k, ""
	}
	e.address = d.cpaAddress
	if d.cpaAddress != "" {
		e.notice = T("这是 CPA-Manager-Plus 的地址：已切换为 Manager-Plus，并从它读到了所连接的 CPA 地址。请填写该 CPA 的管理密钥后保存（若地址只能在容器内访问，请改为本机可访问的地址）。",
			"This is CPA-Manager-Plus: switched to Manager-Plus, and filled in the CPA it collects from. Enter that CPA's management key and save (replace the address if it is only reachable inside a container).")
	} else {
		e.notice = T("这是 CPA-Manager-Plus 的地址：已切换为 Manager-Plus 并填入。CPA-Bar 仍直接管理 CPA，请填写它所连接的 CPA 地址与管理密钥后保存。",
			"This is CPA-Manager-Plus: switched to Manager-Plus and filled it in. CPA-Bar still manages CPA directly: enter the address and management key of the CPA it collects from, and save.")
	}
}

type instancesState struct {
	editor instanceEditor
	// imports waits for keys: an imported file has none.
	imports []store.ImportedInstance
	// remove is the instance a delete asks about.
	remove     string
	removeOpen bool
	tests      map[string]*Task
	op         Task
}

func instancesPage(c *ui.Context, env *PageEnv) {
	a := env.App
	t := c.Theme()
	st := StateFor(a, "instances", "", func() *instancesState { return &instancesState{tests: map[string]*Task{}} })
	settings := a.Svc.Settings()

	PageHeader(c, T("实例", "Instances"), T("CPA-Bar 只连接已在运行的 CPA 服务，不会安装、启动或停止它们。", "CPA-Bar connects to CPA servers that already run; it never installs, starts or stops them."), func() {
		if PillButton(c, icPlus, T("导入", "Import"), false).Clicked() {
			importInstances(env, st)
		}
		if PillButton(c, icExternal, T("导出", "Export"), len(settings.Instances) == 0).Tooltip(T("导出地址与证书，不含管理密钥", "Exports addresses and certificates, never keys")).Clicked() {
			exportInstances(env, settings.Instances)
		}
		if ui.PrimaryButton(c, T("添加实例", "Add Instance")).Radius(999).Clicked() {
			st.editor = instanceEditor{open: true, flavor: flavorAuto}
		}
	})
	TaskStatus(c, &st.op, T("正在保存…", "Saving…"), "")

	if len(settings.Instances) == 0 {
		EmptyState(c, T("还没有实例", "No instances yet"),
			T("点击“添加实例”，填写 CPA 的地址（如 http://127.0.0.1:8317）和管理密钥（remote-management.secret-key）。", "Click Add Instance and enter the CPA address (e.g. http://127.0.0.1:8317) and its management key (remote-management.secret-key)."))
	}
	for i, p := range settings.Instances {
		snap, _ := a.Svc.Snapshot(p.ID)
		test := st.tests[p.ID]
		if test == nil {
			test = &Task{}
			st.tests[p.ID] = test
		}
		now := c.Now()
		Panel(c).Key(p.ID).Gap(12).Children(func() {
			ui.Row(c).Gap(12).AlignItems(ui.Center).Children(func() {
				instanceTile(c, snap, 40, pal(t).Card)
				ui.Column(c).Grow(1).Basis(0).Gap(3).Children(func() {
					ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
						ui.Text(c, p.Name).FontSize(15).FontWeight(600).SingleLine()
						if settings.DefaultInstanceID == p.ID {
							Pill(c, T("默认", "Default"), t.Accent)
						}
						if p.Paused {
							Pill(c, T("已暂停", "Paused"), t.TextMuted)
						}
						if p.ManagerPlus() {
							Pill(c, "Manager-Plus", TintPurple.Text(t)).Tooltip(T("用量从 CPA-Manager-Plus 读取", "Usage is read from CPA-Manager-Plus"))
						}
						if p.CACertPEM != "" {
							Pill(c, T("自定义 CA", "Custom CA"), TintIndigo.Text(t))
						}
					})
					ui.Text(c, PrivURL(cpa.RootURL(p.BaseURL))).FontSize(12).TextColor(t.TextMuted).Selectable().SingleLine()
				})
				Pill(c, ConnLabel(snap.Conn), ConnColor(t, snap.Conn))
			})
			Metrics(c,
				Metric{Label: T("账号", "Accounts"), Value: pick(snap.HasData, staleNumIf(snap.Accounts.Available, snap)+" / "+staleNumIf(snap.Accounts.Total, snap), "—")},
				Metric{Label: T("异常", "Abnormal"), Value: staleNumIf(snap.Accounts.Abnormal(), snap), Color: pick(snap.Accounts.Abnormal() > 0, t.Warning, t.Text)},
				Metric{Label: T("最近检查", "Last check"), Value: Ago(snap.LastAttempt, now)},
				Metric{Label: T("服务器版本", "Server"), Value: pick(snap.Server.Version != "", snap.Server.Version, "—")},
			)
			if snap.Err != nil {
				ui.Text(c, ErrorText(snap.Err)).FontSize(12).TextColor(ConnColor(t, snap.Conn)).MaxLines(3).Selectable()
			}
			ui.Box(c).Height(1).Background(pal(t).Line)
			ui.Row(c).Gap(6).AlignItems(ui.Center).Wrap().Children(func() {
				if PillButton(c, icGear, T("编辑", "Edit"), false).Clicked() {
					st.editor = editorFor(p)
				}
				if PillButton(c, icActivity, T("测试", "Test"), test.Running).Clicked() {
					id := p.ID
					Run(env.Win, test, 0, func(ctx context.Context) error {
						return a.Svc.TestConnection(ctx, core.InstanceInput{Name: p.Name, Address: p.BaseURL, CACertPEM: p.CACertPEM, ForwardAuthOnRedirect: p.ForwardAuthOnRedirect, Flavor: p.Flavor, ManagerURL: p.ManagerURL}, id)
					}, nil)
				}
				pause, pauseIcon := T("暂停", "Pause"), icPause
				if p.Paused {
					pause, pauseIcon = T("恢复", "Resume"), icRefresh
				}
				if PillButton(c, pauseIcon, pause, false).Tooltip(T("暂停后不再自动刷新此实例", "A paused instance is not refreshed automatically")).Clicked() {
					st.op.Err = a.Svc.SetPaused(p.ID, !p.Paused)
				}
				if settings.DefaultInstanceID != p.ID && PillButton(c, icCheck, T("设为默认", "Make Default"), false).Tooltip(T("托盘面板默认打开此实例", "The tray panel opens on this instance")).Clicked() {
					st.op.Err = a.Svc.SetDefaultInstance(p.ID)
				}
				if settings.DefaultInstanceID == p.ID && PillButton(c, icBan, T("取消默认", "Unset Default"), false).Clicked() {
					st.op.Err = a.Svc.SetDefaultInstance("")
				}
				if PillButton(c, icChevronUp, "", i == 0).Label(T("上移", "Move up")).Tooltip(T("上移", "Move up")).Clicked() {
					st.op.Err = a.Svc.MoveInstance(p.ID, -1)
				}
				if PillButton(c, icChevronDown, "", i == len(settings.Instances)-1).Label(T("下移", "Move down")).Tooltip(T("下移", "Move down")).Clicked() {
					st.op.Err = a.Svc.MoveInstance(p.ID, 1)
				}
				ui.Box(c).Grow(1)
				TaskStatus(c, test, T("正在测试…", "Testing…"), T("连接正常", "Connection OK"))
				if ui.Button(c, T("删除", "Delete")).TextColor(t.Danger).Radius(999).Clicked() {
					st.remove, st.removeOpen = p.ID, true
				}
			})
		})
	}

	if st.removeOpen {
		if p := settings.Instance(st.remove); p != nil {
			msg := fmt.Sprintf(T("将从 CPA-Bar 中移除“%s”并删除系统钥匙串中保存的管理密钥。服务器上的账号和配置不受影响。", "This removes “%s” from CPA-Bar and deletes its management key from the system credential store. Accounts and configuration on the server are not touched."), p.Name)
			if ui.AlertDialog(c, &st.removeOpen, T("删除实例？", "Remove instance?"), msg, T("取消", "Cancel"), T("确认删除", "Remove")) == 1 {
				id := p.ID
				st.op.Err = a.Svc.RemoveInstance(id)
				if st.op.Err == nil {
					a.dropInstanceState(id)
					delete(st.tests, id)
				}
			}
		} else {
			st.removeOpen = false
		}
	}

	instanceEditorView(c, env, st)
}

func instanceEditorView(c *ui.Context, env *PageEnv, st *instancesState) {
	a := env.App
	t := c.Theme()
	e := &st.editor
	if !e.open && len(st.imports) > 0 {
		// Imported instances come one at a time, each needing its key.
		next := st.imports[0]
		st.imports = st.imports[1:]
		*e = editorFor(store.InstanceProfile{Name: next.Name, BaseURL: next.BaseURL, CACertPEM: next.CACertPEM, ForwardAuthOnRedirect: next.ForwardAuthOnRedirect, Icon: next.Icon, Color: next.Color, Flavor: next.Flavor, ManagerURL: next.ManagerURL})
	}
	if !e.open {
		// The keys typed in are not kept once the dialog closes.
		e.key, e.managerKey = "", ""
		return
	}
	busy := e.test.Running || e.save.Running
	ui.Modal(c, &e.open, func() {
		ui.Column(c).Width(520).Padding(20).Gap(12).Children(func() {
			title := T("添加实例", "Add Instance")
			if e.id != "" {
				title = T("编辑实例", "Edit Instance")
			}
			ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
				markTile(c, e.icon, e.color, 30)
				ui.Text(c, title).FontSize(17).Bold()
			})
			if e.id == "" && len(st.imports) > 0 {
				ui.Text(c, fmt.Sprintf(T("导入中：还有 %d 个实例等待填写密钥", "Importing: %d more instances wait for their keys"), len(st.imports))).FontSize(12).TextColor(t.TextMuted)
			}
			ui.Scroll(c.Key("editor-scroll")).MaxHeight(dialogHeight(c, 640, 200)).Gap(12).Children(func() {
				instanceEditorFields(c, e)
			})
			if e.notice != "" {
				ui.Row(c).Gap(8).Padding(10, 12).Radius(10).Background(t.Accent.Alpha(0.1)).Children(func() {
					ui.Icon(c, icInfo).FontSize(14).TextColor(t.Accent)
					ui.Text(c, e.notice).FontSize(12).Grow(1).Basis(0)
				})
			}

			TaskStatus(c, &e.test, T("正在测试连接…", "Testing connection…"), T("连接正常，管理接口可用", "Connected; the management API answers"))
			ErrorBanner(c, e.save.Err)
			if e.save.Running {
				Busy(c, T("正在验证并保存…", "Verifying and saving…"))
			}

			ui.Row(c).Gap(8).Children(func() {
				if ui.Button(c, T("测试连接", "Test Connection")).Disabled(busy || strings.TrimSpace(e.address) == "").Clicked() {
					in, id, auto := e.input(), e.id, e.flavor == flavorAuto
					e.notice = ""
					Run(env.Win, &e.test, 0, func(ctx context.Context) error {
						if auto {
							if err := detect(ctx, a.Svc, in); err != nil {
								return err
							}
						}
						return a.Svc.TestConnection(ctx, in, id)
					}, func(err error) { e.applyDetection(&e.test, err) })
				}
				ui.Box(c).Grow(1)
				cancel := T("取消", "Cancel")
				if len(st.imports) > 0 && e.id == "" {
					cancel = T("跳过", "Skip")
				}
				if ui.Button(c, cancel).Disabled(e.save.Running).Clicked() {
					e.open = false
				}
				save := T("添加", "Add")
				if e.id != "" {
					save = T("保存", "Save")
				}
				ready := strings.TrimSpace(e.address) != "" && (e.id != "" || strings.TrimSpace(e.key) != "")
				if e.flavor == flavorPlus {
					ready = ready && strings.TrimSpace(e.managerURL) != "" && (e.id != "" || strings.TrimSpace(e.managerKey) != "")
				}
				if ui.PrimaryButton(c, save).Disabled(busy || !ready).Clicked() {
					in, id, auto := e.input(), e.id, e.flavor == flavorAuto
					e.notice = ""
					Run(env.Win, &e.save, 0, func(ctx context.Context) error {
						if auto {
							if err := detect(ctx, a.Svc, in); err != nil {
								return err
							}
						}
						if id == "" {
							newID, err := a.Svc.AddInstance(ctx, in)
							if err == nil {
								update(env.Win, func() { a.selected = newID })
							}
							return err
						}
						return a.Svc.UpdateInstance(id, in)
					}, func(err error) {
						if err == nil {
							e.open = false
						}
						e.applyDetection(&e.save, err)
					})
				}
			})
		})
	})
}

// addressWarning warns when a key would cross the network in clear text.
func addressWarning(addr string) string {
	base, err := cpa.NormalizeManagementURL(addr)
	if err != nil {
		if strings.TrimSpace(addr) == "" {
			return ""
		}
		return ErrorText(err)
	}
	u, err := url.Parse(base)
	if err != nil || u.Scheme != "http" {
		return ""
	}
	switch h := u.Hostname(); {
	case h == "localhost", strings.HasPrefix(h, "127."), h == "::1":
		return ""
	}
	return T("此地址使用 HTTP，管理密钥将以明文在网络上传输。远程实例建议使用 HTTPS。", "This address uses HTTP: the management key would cross the network in clear text. Use HTTPS for remote instances.")
}

var jsonFilter = []mygo.FileFilter{{Name: "JSON", Extensions: []string{"json"}}}

func exportInstances(env *PageEnv, profiles []store.InstanceProfile) {
	SaveFile(env.Win, T("导出实例", "Export Instances"), "cpa-bar-instances.json", jsonFilter, func(path string) {
		st := StateFor(env.App, "instances", "", func() *instancesState { return &instancesState{tests: map[string]*Task{}} })
		b, err := store.ExportInstances(profiles)
		if err == nil {
			err = os.WriteFile(path, b, 0o600)
		}
		st.op.Err = err
		if err == nil {
			st.op.Done = true
			go mygo.Dialog.Message(mygo.MessageOptions{Parent: liveWindow(env.Win), Message: T("已导出实例", "Instances exported"),
				Detail: T("文件不包含管理密钥，导入时需要重新填写。", "The file holds no management keys; they are asked again on import.")})
		}
	})
}

func importInstances(env *PageEnv, st *instancesState) {
	OpenFile(env.Win, T("导入实例", "Import Instances"), jsonFilter, func(path string) {
		b, err := os.ReadFile(path)
		var list []store.ImportedInstance
		if err == nil {
			list, err = store.ParseInstances(b)
		}
		st.op.Err = err
		if err == nil && len(list) == 0 {
			st.op.Err = fmt.Errorf("%s", T("文件中没有实例", "The file has no instances"))
		}
		st.imports = append(st.imports, list...)
	})
}

// instanceEditorFields are the fields of the instance editor.
func instanceEditorFields(c *ui.Context, e *instanceEditor) {
	t := c.Theme()
	ui.Form(c, func() {
		ui.Field(c, T("名称", "Name"), func() {
			ui.TextInput(c.Key("name"), &e.name).Placeholder(T("可选，默认使用地址", "Optional; defaults to the address"))
		})
		ui.Field(c, T("类型", "Server"), func() {
			ui.Column(c).Gap(6).Children(func() {
				ui.Row(c.Key("flavor")).Children(func() {
					if PillTabs(c, &e.flavor, T("自动检测", "Detect"), T("官方 CPA", "Official CPA"), "CPA-Manager-Plus") {
						e.notice = ""
					}
				})
				var help string
				switch e.flavor {
				case flavorAuto:
					help = T("测试或保存时检测地址：若是 CPA-Manager-Plus，或同一主机上有它，会切换并提示补全。", "Test or save detects the address: if it is CPA-Manager-Plus, or one runs on the same host, the form switches and asks for the rest.")
				case flavorPlus:
					help = T("CPA-Bar 仍通过 CPA 的管理接口管理账号与配置；用量改从 CPA-Manager-Plus 读取，包括它保存的历史，不再订阅 CPA 的用量流。", "CPA-Bar still manages accounts and configuration through CPA's management API; usage is read from CPA-Manager-Plus, history included, instead of CPA's usage stream.")
				}
				if help != "" {
					ui.Text(c, help).FontSize(11).TextColor(t.TextMuted)
				}
			})
		})
		addr := T("地址", "Address")
		if e.flavor == flavorPlus {
			addr = T("CPA 地址", "CPA address")
		}
		ui.Field(c, addr, func() {
			ui.TextInput(c.Key("address"), &e.address).Placeholder("http://127.0.0.1:8317").AutoFocus()
		})
		ui.Field(c, T("管理密钥", "Management key"), func() {
			ph := T("remote-management.secret-key 的明文值", "The plain value of remote-management.secret-key")
			if e.id != "" {
				ph = T("留空则保持已保存的密钥", "Leave empty to keep the stored key")
			}
			ui.TextInput(c.Key("key"), &e.key).Password().Placeholder(ph)
		})
		if e.flavor == flavorPlus {
			ui.Field(c, T("Manager-Plus 地址", "Manager-Plus address"), func() {
				ui.TextInput(c.Key("manager-url"), &e.managerURL).Placeholder("http://127.0.0.1:" + cpamp.DefaultPort)
			})
			ui.Field(c, T("管理员密钥", "Admin key"), func() {
				ph := T("Manager-Plus 的 Admin Key（cpamp_…）", "The Manager-Plus admin key (cpamp_…)")
				if e.id != "" {
					ph = T("留空则保持已保存的密钥", "Leave empty to keep the stored key")
				}
				ui.TextInput(c.Key("manager-key"), &e.managerKey).Password().Placeholder(ph)
			})
		}
		ui.Field(c, T("标识", "Mark"), func() {
			markPicker(c, &e.icon, &e.color)
		})
	})
	if warn := addressWarning(e.address); warn != "" {
		ui.Text(c, warn).FontSize(12).TextColor(t.Warning)
	}
	if e.flavor == flavorPlus {
		if warn := managerWarning(e.managerURL); warn != "" {
			ui.Text(c, warn).FontSize(12).TextColor(t.Warning)
		}
	}
	ui.Checkbox(c.Key("adv"), &e.advanced, T("高级设置", "Advanced settings"))
	if e.advanced {
		forwardLabel := T("重定向时继续携带授权信息", "Keep authorization on redirects")
		ui.Row(c).Gap(12).AlignItems(ui.Center).Children(func() {
			ui.Text(c, forwardLabel).Grow(1).Basis(0)
			ui.Switch(c.Key("forward-auth-on-redirect"), &e.forwardAuthOnRedirect).Label(forwardLabel)
		})
		warningColor := t.TextMuted
		if e.forwardAuthOnRedirect {
			warningColor = t.Warning
		}
		ui.Text(c, T("开启后，CPA 管理密钥或 Manager-Plus 管理员密钥会发送到重定向目标，包括其他域名；从 HTTPS 重定向到 HTTP 始终禁止。", "When enabled, the CPA management key or Manager-Plus admin key is sent to redirect targets, including other domains; redirects from HTTPS to HTTP are always blocked.")).FontSize(11).TextColor(warningColor)
		ui.Text(c, T("自定义 CA 证书（自签名 HTTPS）", "Custom CA certificate (self-signed HTTPS)")).FontSize(12).FontWeight(600)
		ui.TextArea(c.Key("ca"), &e.ca).Placeholder("-----BEGIN CERTIFICATE-----").Height(120).Font("monospace").FontSize(11)
		ui.Text(c, T("证书校验始终开启；自定义 CA 只是额外信任此证书。", "Certificate verification always stays on; a custom CA is only trusted in addition.")).FontSize(11).TextColor(t.TextMuted)
	}
	ui.Text(c, T("密钥只保存在系统钥匙串中，不会写入配置文件或日志。", "The keys are kept only in the system credential store, never in files or logs.")).FontSize(11).TextColor(t.TextMuted)
}

// managerWarning warns when the admin key would cross the network in
// clear text.
func managerWarning(addr string) string {
	root, err := cpamp.NormalizeURL(addr)
	if err != nil {
		if strings.TrimSpace(addr) == "" {
			return ""
		}
		return ErrorText(err)
	}
	return addressWarning(root)
}
