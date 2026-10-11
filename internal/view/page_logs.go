package view

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/licoy/cpa-bar/internal/cpa"
)

func init() {
	RegisterPage(Page{
		ID:       "logs",
		Order:    10,
		Group:    GroupMonitor,
		Title:    func() string { return T("日志与诊断", "Logs & Diagnostics") },
		Instance: true,
		View:     logsPage,
	})
}

const (
	// logsPollEvery is how often the live log asks for new lines while it
	// is on screen.
	logsPollEvery = 3 * time.Second
	// logsErrorBackoff spaces the retries after a failed read.
	logsErrorBackoff = 10 * time.Second
	// logsMaxLines caps the lines kept in memory per instance.
	logsMaxLines = 5000
	// logsTailLimit and logsDeltaLimit are the line limits of the first
	// read (the tail) and of the incremental ones.
	logsTailLimit  = 500
	logsDeltaLimit = 1000
	// logsViewMaxBytes and logsViewMaxLines cap what a viewer shows; the
	// download always gets the whole file.
	logsViewMaxBytes = 1 << 20
	logsViewMaxLines = 5000
	logsMaxLineRunes = 2000
	// logsListLeast is the least height of the live log, which otherwise
	// takes the height the window leaves.
	logsListLeast = 260
)

// logs tabs.
const (
	logsTabLive = iota
	logsTabFiles
	logsTabDiag
)

// logsViewer is a read-only text shown in a dialog.
type logsViewer struct {
	open  bool
	title string
	lines []string
	note  string
	doc   *logsDoc
}

func (v *logsViewer) show(title string, lines []string, note string) {
	*v = logsViewer{open: true, title: title, lines: lines, note: note, doc: newLogsDoc(lines)}
}

type logsErrFile struct {
	Name     string `json:"name"`
	Size     int64  `json:"size"`
	Modified int64  `json:"modified"`
}

type logsState struct {
	tab int

	// Live log.
	lines    []string
	cursor   string
	gen      int // bumped whenever lines change
	loaded   bool
	fetch    Task
	nextAt   time.Time
	err      error
	disabled bool
	paused   bool
	filter   string
	doc      logsDoc // the text area showing visible
	visible  []string
	visGen   int
	visVer   int // bumped whenever visible is recomputed
	visFor   string
	visInit  bool
	clear    Task
	clearAsk bool

	// Error log files and request logs.
	files      []logsErrFile
	filesLoad  Task
	filesReady bool
	view       Task
	download   Task
	reqID      string
	viewer     logsViewer

	// Diagnostics.
	latest     Task
	latestText string
	latestTint Tint
	apiMethod  string
	apiURL     string
	apiHeaders string
	apiBody    string
	apiCred    string
	apiAsk     bool
	apiTask    Task
	apiResult  *logsAPIResult
}

type logsAPIResult struct {
	status int
	lines  []string
	note   string
	doc    *logsDoc // lines as a text, made when first shown
}

func logsPage(c *ui.Context, env *PageEnv) {
	st := State(env, "logs", func() *logsState { return &logsState{apiMethod: "GET"} })
	PageHeader(c, T("日志与诊断", "Logs & Diagnostics"),
		T("读取并诊断当前实例：服务日志、请求错误日志、版本与上游连通性。", "Read and diagnose the selected instance: server logs, request error logs, version and upstream reachability."), nil)

	ui.Row(c).Children(func() {
		PillTabs(c.Key("logs-tab"), &st.tab,
			T("实时日志", "Live log"), T("错误与请求日志", "Error & request logs"), T("诊断", "Diagnostics"))
	})

	switch st.tab {
	case logsTabLive:
		logsLive(c, env, st)
	case logsTabFiles:
		logsFilesTab(c, env, st)
	default:
		logsDiagTab(c, env, st)
	}
	logsViewerDialog(c, st)
}

// ---- Live log ----

func logsLive(c *ui.Context, env *PageEnv, st *logsState) {
	t := c.Theme()
	logsPoll(c, env, st)

	ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
		ui.TextInput(c.Key("logs-filter"), &st.filter).Placeholder(T("过滤日志（不区分大小写）", "Filter lines (case-insensitive)")).Grow(1)
		pause, icon := T("暂停", "Pause"), icPause
		tip := T("暂停自动刷新与自动滚动", "Stop refreshing and following the end")
		if st.paused {
			pause, icon = T("继续", "Resume"), icRefresh
			tip = T("恢复自动刷新并跟随最新日志", "Refresh again and follow the newest lines")
		}
		if PillButton(c, icon, pause, false).Tooltip(tip).Clicked() {
			st.paused = !st.paused
			if !st.paused {
				st.nextAt = time.Time{}
			}
		}
		if PillButton(c, icCopy, T("复制可见日志", "Copy Visible"), len(st.visible) == 0).Clicked() {
			c.WriteClipboard(strings.Join(st.visible, "\n"))
			c.Toast(fmt.Sprintf(T("已复制 %d 行", "Copied %d lines"), len(st.visible)))
		}
		if logsDangerPill(c, icTrash, T("清空日志", "Clear Logs"), env.Client == nil || st.clear.Running || st.disabled).Clicked() {
			st.clearAsk = true
		}
	})

	if st.disabled {
		Notice(c, T("此实例没有把日志写入文件，所以管理接口无法读取日志。请在 CPA 配置中开启 logging-to-file: true（v8 配置布局中为 observability.logs.logging-to-file），保存后点击“重试”。",
			"This instance does not write logs to a file, so the management API cannot read them. Set logging-to-file: true in the CPA configuration (observability.logs.logging-to-file in the v8 layout), save, then click Retry."))
		ui.Row(c).Children(func() {
			if PillButton(c, icRefresh, T("重试", "Retry"), false).Clicked() {
				st.disabled = false
				st.nextAt = time.Time{}
			}
		})
	} else {
		ErrorBanner(c, st.err)
	}
	TaskStatus(c, &st.clear, T("正在清空日志…", "Clearing logs…"), fmt.Sprintf(T("已清空“%s”的日志", "Cleared the logs of “%s”"), env.InstanceName()))

	if st.clearAsk {
		msg := fmt.Sprintf(T("将删除实例“%s”服务器上的全部历史日志文件，并清空当前日志文件。此操作不可恢复。", "This deletes every rotated log file on the server of “%s” and empties the current one. It cannot be undone."), env.InstanceName())
		if ui.AlertDialog(c, &st.clearAsk, fmt.Sprintf(T("清空“%s”的日志？", "Clear the logs of “%s”?"), env.InstanceName()), msg, T("取消", "Cancel"), T("清空", "Clear")) == 1 {
			logsClear(env, st)
		}
	}

	vis := logsVisible(st)
	state := T("每 3 秒自动刷新", "Refreshing every 3 s")
	if st.paused {
		state = T("已暂停", "Paused")
	} else if st.disabled {
		state = T("未开启日志文件", "Log file off")
	}
	if st.doc.holding && st.doc.ver != st.visVer {
		state = T("已选中文字，新日志稍后显示", "Text selected, new lines show once it is let go")
	}
	ui.Row(c).Gap(8).AlignItems(ui.Center).Padding(0, 4).Children(func() {
		ui.Text(c, fmt.Sprintf(T("共 %d 行，显示 %d 行（最多保留 %d 行）", "%d lines, %d shown (keeps at most %d)"), len(st.lines), len(vis), logsMaxLines)).
			FontSize(12).TextColor(t.TextMuted)
		ui.Text(c, "·").FontSize(12).TextColor(t.TextMuted)
		ui.Text(c, state).FontSize(12).TextColor(t.TextMuted)
		if st.fetch.Running {
			ui.Spinner(c).Label(T("正在读取", "Reading"))
		}
	})

	if len(vis) == 0 {
		msg := T("暂无日志", "No log lines yet")
		if !st.loaded && !st.disabled && st.err == nil {
			msg = T("正在读取…", "Reading…")
		} else if st.filter != "" && len(st.lines) > 0 {
			msg = T("没有匹配过滤条件的日志", "No line matches the filter")
		}
		st.doc.holding = false
		fillHeight(ui.Column(c), logsListLeast).Radius(14).Background(pal(t).Card).Children(func() {
			EmptyState(c, msg, "")
		})
		return
	}
	// New lines replace the text only while no selection is held in it (it
	// would be lost), and the view follows the end only while the user is
	// at the end and the log is not paused.
	if (st.doc.ver != st.visVer || st.doc.text == "" || st.doc.private != Private()) && !st.doc.holding {
		st.doc.set(vis, st.visVer)
	}
	logsMono(c, "logs-list-"+env.InstanceID, &st.doc, 0, pal(t).Card, !st.paused && !st.doc.holding)
}

// logsPoll fetches new lines when they are due. It runs from the page's
// build, so nothing is fetched while another page or no window shows.
func logsPoll(c *ui.Context, env *PageEnv, st *logsState) {
	if env.Client == nil || st.paused || st.disabled || st.fetch.Running || st.clear.Running {
		return
	}
	if m := env.App.manager; m != nil && !m.IsVisible() {
		return
	}
	now := c.Now()
	if now.Before(st.nextAt) {
		c.After(st.nextAt.Sub(now))
		return
	}
	cl, cursor := env.Client, st.cursor
	limit := logsTailLimit
	if cursor != "" {
		limit = logsDeltaLimit
	}
	var page *cpa.LogPage
	Run(env.Win, &st.fetch, 0, func(ctx context.Context) error {
		p, err := cl.Logs(ctx, cursor, limit)
		page = p
		return err
	}, func(err error) {
		st.applyFetch(page, err, cursor, limit)
	})
}

// applyFetch takes the result of a read of lines after cursor.
func (st *logsState) applyFetch(page *cpa.LogPage, err error, cursor string, limit int) {
	now := time.Now()
	if err != nil {
		st.nextAt = now.Add(logsErrorBackoff)
		if logsDisabledErr(err) {
			st.disabled, st.err = true, nil
			return
		}
		st.err = err
		return
	}
	st.err, st.disabled, st.loaded = nil, false, true
	st.nextAt = now.Add(logsPollEvery)
	if page == nil {
		return
	}
	if cursor == "" || page.CursorReset {
		// A tail, or the log rotated away from the cursor: start over.
		st.lines = append([]string(nil), page.Lines...)
	} else if len(page.Lines) > 0 {
		st.lines = append(st.lines, page.Lines...)
		if cursor != "" && len(page.Lines) >= limit {
			st.nextAt = now // more is waiting behind this page
		}
	}
	if over := len(st.lines) - logsMaxLines; over > 0 {
		st.lines = append([]string(nil), st.lines[over:]...)
	}
	if page.NextCursor != "" {
		st.cursor = page.NextCursor
	}
	st.gen++
}

// logsDisabledErr reports the server's answer when it keeps no log file.
func logsDisabledErr(err error) bool {
	var ae *cpa.APIError
	return errors.As(err, &ae) && strings.Contains(strings.ToLower(ae.Message), "logging to file disabled")
}

func logsClear(env *PageEnv, st *logsState) {
	cl := env.Client
	if cl == nil {
		return
	}
	Run(env.Win, &st.clear, 0, func(ctx context.Context) error { return cl.ClearLogs(ctx) }, func(err error) {
		if err == nil {
			st.lines, st.cursor, st.loaded = nil, "", false
			st.gen++
			st.nextAt = time.Time{}
			return
		}
		if logsDisabledErr(err) {
			st.disabled, st.clear.Err = true, nil
		}
	})
}

// logsVisible is the buffer through the filter, kept between frames.
func logsVisible(st *logsState) []string {
	if st.visInit && st.visGen == st.gen && st.visFor == st.filter {
		return st.visible
	}
	st.visInit, st.visGen, st.visFor = true, st.gen, st.filter
	st.visVer++
	f := strings.ToLower(strings.TrimSpace(st.filter))
	if f == "" {
		st.visible = st.lines
		return st.visible
	}
	out := make([]string, 0, len(st.lines)/4)
	for _, l := range st.lines {
		if strings.Contains(strings.ToLower(l), f) {
			out = append(out, l)
		}
	}
	st.visible = out
	return out
}

func logsLineLevel(line string) int {
	l := strings.ToLower(line)
	switch {
	case strings.Contains(l, "[error]") || strings.Contains(l, "level=error") || strings.Contains(l, "[fatal]") || strings.Contains(l, "panic"):
		return logsLevelError
	case strings.Contains(l, "[warn") || strings.Contains(l, "level=warn"):
		return logsLevelWarn
	}
	return logsLevelInfo
}

// ---- Error logs and request logs ----

func logsFilesTab(c *ui.Context, env *PageEnv, st *logsState) {
	t := c.Theme()
	cl := env.Client
	if !st.filesReady && !st.filesLoad.Running && cl != nil && st.filesLoad.Err == nil {
		logsLoadFiles(env, st)
	}

	Section(c, T("请求错误日志", "Request error logs"), func() {
		ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
			ui.Text(c, T("请求失败时服务器会保存 error-*.log 文件；开启 request-log 后则改为保存全部请求日志，这里为空。", "The server keeps error-*.log files for failed requests; with request-log on it keeps every request log instead and this list is empty.")).
				FontSize(12).TextColor(t.TextMuted).Grow(1).Basis(0)
			if BusyPillButton(c, icRefresh, T("刷新", "Reload"), st.filesLoad.Running, cl == nil).Clicked() {
				logsLoadFiles(env, st)
			}
		})
		ErrorBanner(c, st.filesLoad.Err)
		TaskStatus(c, &st.download, T("正在下载…", "Downloading…"), T("已保存", "Saved"))
		if st.filesReady && len(st.files) == 0 {
			Card(c).Children(func() {
				ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
					ui.Icon(c, icCheck).FontSize(16).TextColor(t.Success)
					ui.Text(c, T("没有错误日志文件。", "No error log files.")).FontSize(13).TextColor(t.TextMuted)
				})
			})
		}
		if len(st.files) > 0 {
			logsListCard(c, len(st.files), func(i int) {
				f := st.files[i]
				ui.Row(c).Key(f.Name).Gap(12).Padding(10, 14).AlignItems(ui.Center).Children(func() {
					Tile(c, icFileCog, TintOrange, 34, false)
					ui.Column(c).Grow(1).Basis(0).Gap(2).Children(func() {
						ui.Text(c, f.Name).FontSize(13).FontWeight(500).Font("monospace").SingleLine().Selectable()
						ui.Text(c, Ago(time.Unix(f.Modified, 0), env.Now)).FontSize(12).TextColor(t.TextMuted).SingleLine()
					})
					Pill(c, logsBytes(f.Size), TintGray.Text(t))
					if PillButton(c, nil, T("查看", "View"), st.view.Running).Clicked() {
						name := f.Name
						logsOpen(env, st, name, func(ctx context.Context) ([]byte, error) { return cl.DownloadErrorLog(ctx, name) })
					}
					if PillButton(c, nil, T("下载", "Download"), st.download.Running).Clicked() {
						logsDownload(env, st, f.Name)
					}
				})
			})
		}
	})

	Section(c, T("按请求 ID 查看请求日志", "Request log by ID"), func() {
		Card(c).Children(func() {
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.TextInput(c.Key("logs-reqid"), &st.reqID).Placeholder(T("请求 ID（响应头 X-Request-ID 或日志中的 ID）", "Request ID (X-Request-ID header or the ID in the log)")).Grow(1)
				id := strings.TrimSpace(st.reqID)
				if ui.PrimaryButton(c, T("查看", "View")).Disabled(cl == nil || id == "" || st.view.Running).Clicked() {
					logsOpen(env, st, id, func(ctx context.Context) ([]byte, error) { return cl.RequestLog(ctx, id) })
				}
			})
			ui.Text(c, T("只有服务器保存了该请求的日志文件时才能查到（需开启 request-log，或该请求出错）。", "Found only when the server kept a log file for that request (request-log on, or the request failed).")).
				FontSize(12).TextColor(t.TextMuted)
			if st.view.Running {
				Busy(c, T("正在读取…", "Loading…"))
			}
			ErrorBanner(c, st.view.Err)
		})
	})
}

func logsLoadFiles(env *PageEnv, st *logsState) {
	cl := env.Client
	if cl == nil {
		return
	}
	var raw json.RawMessage
	Run(env.Win, &st.filesLoad, 0, func(ctx context.Context) error {
		var err error
		raw, err = cl.ErrorLogs(ctx)
		return err
	}, func(err error) {
		if err != nil {
			return
		}
		files, perr := logsParseErrorLogs(raw)
		if perr != nil {
			st.filesLoad.Err, st.filesLoad.Done = perr, false
			return
		}
		st.files, st.filesReady = files, true
	})
}

// logsParseErrorLogs reads {"files":[{name,size,modified}]}, or a bare
// array of the same.
func logsParseErrorLogs(raw json.RawMessage) ([]logsErrFile, error) {
	var obj struct {
		Files []logsErrFile `json:"files"`
	}
	var files []logsErrFile
	if err := json.Unmarshal(raw, &obj); err == nil {
		files = obj.Files
	} else if err := json.Unmarshal(raw, &files); err != nil {
		return nil, fmt.Errorf("%s: %w", T("无法解析错误日志列表", "Cannot read the error log list"), err)
	}
	sort.SliceStable(files, func(i, j int) bool { return files[i].Modified > files[j].Modified })
	return files, nil
}

// logsOpen loads a text and shows it in the viewer.
func logsOpen(env *PageEnv, st *logsState, title string, load func(ctx context.Context) ([]byte, error)) {
	var body []byte
	Run(env.Win, &st.view, 0, func(ctx context.Context) error {
		var err error
		body, err = load(ctx)
		return err
	}, func(err error) {
		if err != nil {
			return
		}
		lines, note := logsSplit(body)
		st.viewer.show(title, lines, note)
	})
}

// logsDownload asks where to save an error log, then saves it with the
// permissions of a private file.
func logsDownload(env *PageEnv, st *logsState, name string) {
	cl := env.Client
	if cl == nil {
		return
	}
	SaveFile(env.Win, T("保存错误日志", "Save Error Log"), name, nil, func(path string) {
		Run(env.Win, &st.download, 0, func(ctx context.Context) error {
			b, err := cl.DownloadErrorLog(ctx, name)
			if err != nil {
				return err
			}
			return os.WriteFile(path, b, 0o600)
		}, nil)
	})
}

// logsSplit makes lines of a downloaded text, capped in size so that a huge
// file cannot stall the window.
func logsSplit(b []byte) (lines []string, note string) {
	truncated := false
	if len(b) > logsViewMaxBytes {
		b, truncated = b[:logsViewMaxBytes], true
	}
	s := strings.ToValidUTF8(string(b), "�")
	s = strings.ReplaceAll(s, "\r\n", "\n")
	all := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(all) > logsViewMaxLines {
		all, truncated = all[:logsViewMaxLines], true
	}
	for i, l := range all {
		if r := []rune(l); len(r) > logsMaxLineRunes {
			all[i] = string(r[:logsMaxLineRunes]) + "…"
			truncated = true
		}
	}
	if truncated {
		note = T("内容较大，仅显示开头部分；下载可得到完整文件。", "The content is large, only its start is shown; download for the whole file.")
	}
	return all, note
}

func logsViewerDialog(c *ui.Context, st *logsState) {
	v := &st.viewer
	if !v.open {
		return
	}
	t := c.Theme()
	ui.Modal(c, &v.open, func() {
		ui.Column(c).Width(780).Padding(20).Gap(12).Children(func() {
			ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
				Tile(c, icFileCog, TintOrange, 30, false)
				ui.Text(c, v.title).FontSize(17).FontWeight(600).SingleLine().Selectable().Grow(1).Basis(0)
			})
			if v.note != "" {
				ui.Text(c, v.note).FontSize(12).TextColor(t.Warning)
			}
			if len(v.lines) == 0 {
				ui.Text(c, T("（空）", "(empty)")).FontSize(12).TextColor(t.TextMuted)
			} else {
				logsMono(c, "logs-viewer", v.doc, dialogHeight(c, 720, 220), pal(t).Card, false)
			}
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				if PillButton(c, icCopy, T("复制", "Copy"), len(v.lines) == 0).Clicked() {
					c.WriteClipboard(strings.Join(v.lines, "\n"))
					c.Toast(T("已复制", "Copied"))
				}
				ui.Box(c).Grow(1)
				if ui.PrimaryButton(c, T("关闭", "Close")).Clicked() {
					v.open = false
				}
			})
		})
	})
}

// ---- Diagnostics ----

var logsAPIMethods = []string{"GET", "POST", "PUT", "PATCH", "DELETE", "HEAD"}

func logsNoCred() string { return T("不使用凭据", "No credential") }

func logsDiagTab(c *ui.Context, env *PageEnv, st *logsState) {
	t := c.Theme()
	s := env.Snap
	cl := env.Client

	Panel(c).Children(func() {
		PanelTitle(c, icServer, TintBlue, T("服务器与连接", "Server & connection"), "", false)
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			Pill(c, ConnLabel(s.Conn), ConnColor(t, s.Conn))
			plug, col := T("插件支持：未知", "Plugins: unknown"), t.TextMuted
			switch s.Server.SupportPlugin {
			case "1":
				plug, col = T("支持插件", "Plugins supported"), t.Success
			case "0":
				plug, col = T("不支持插件（该构建未启用 CGO）", "No plugin support (build without CGO)"), t.Danger
			}
			Pill(c, plug, col)
		})
		ui.Grid(c).Columns(2).GapX(24).GapY(10).Children(func() {
			kv(c, T("实例", "Instance"), s.Profile.Name)
			kv(c, T("管理接口", "Management API"), PrivURL(s.Profile.BaseURL))
			kv(c, T("最近成功", "Last success"), Ago(s.LastSuccess, env.Now))
			kv(c, T("最近检查", "Last attempt"), Ago(s.LastAttempt, env.Now))
			kv(c, T("CPA 版本", "CPA version"), logsOrDash(s.Server.Version))
			kv(c, T("提交", "Commit"), logsOrDash(s.Server.Commit))
			kv(c, T("构建日期", "Build date"), logsOrDash(s.Server.BuildDate))
		})
		if s.Err != nil {
			ui.Text(c, ErrorText(s.Err)).FontSize(12).TextColor(ConnColor(t, s.Conn)).Selectable()
		}
		ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
			if BusyPillButton(c, icRefresh, T("检查最新版本", "Check Latest Version"), st.latest.Running, cl == nil).
				Tooltip(T("由服务器向 GitHub 查询最新发布版本", "The server asks GitHub for the latest release")).Clicked() {
				logsCheckLatest(env, st)
			}
			if !st.latest.Running && st.latestText != "" {
				col := st.latestTint.C(t)
				ui.Row(c).Grow(1).Basis(0).Padding(6, 12).Radius(12).Background(col.Alpha(0.15)).Children(func() {
					ui.Text(c, st.latestText).FontSize(12).FontWeight(600).TextColor(st.latestTint.Text(t)).Selectable()
				})
			}
		})
		ErrorBanner(c, st.latest.Err)
	})

	Panel(c).Children(func() {
		PanelTitle(c, icActivity, TintOrange, T("API 调用测试", "API call test"), "", false)
		Notice(c, fmt.Sprintf(T("此请求由“%s”服务器发出，而不是由本机发出；若选择凭据，服务器会用该凭据发请求，请求头中的 $TOKEN$ 会被替换为该凭据的令牌。请只访问你信任的地址。",
			"This request is sent BY THE SERVER of “%s”, not by this computer. With a credential selected, the server uses it, and $TOKEN$ in the headers is replaced by that credential's token. Only call addresses you trust."), env.InstanceName()))
		creds, labels := logsCredOptions(s.Credentials)
		if _, ok := creds[st.apiCred]; !ok {
			// Also after a language change or when the credential is gone.
			st.apiCred = labels[0]
		}
		ui.Form(c, func() {
			ui.Field(c, T("方法", "Method"), func() {
				ui.Select(c.Key("logs-api-method"), &st.apiMethod, logsAPIMethods)
			})
			ui.Field(c, "URL", func() {
				ui.TextInput(c.Key("logs-api-url"), &st.apiURL).Placeholder("https://api.example.com/v1/ping")
			})
			ui.Field(c, T("请求头（每行 名称: 值）", "Headers (one Name: value per line)"), func() {
				ui.TextArea(c.Key("logs-api-headers"), &st.apiHeaders).Height(70).Font("monospace").FontSize(11).
					Placeholder("Authorization: Bearer $TOKEN$")
			})
			ui.Field(c, T("请求体", "Body"), func() {
				ui.TextArea(c.Key("logs-api-body"), &st.apiBody).Height(90).Font("monospace").FontSize(11)
			})
			ui.Field(c, T("凭据（可选）", "Credential (optional)"), func() {
				ui.Select(c.Key("logs-api-cred"), &st.apiCred, labels)
			})
		})
		payload, perr := logsBuildPayload(st, creds, labels)
		if perr != nil && (strings.TrimSpace(st.apiURL) != "" || strings.TrimSpace(st.apiHeaders) != "") {
			ui.Text(c, perr.Error()).FontSize(12).TextColor(t.Warning)
		}
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ready := cl != nil && perr == nil && !st.apiTask.Running
			if ui.PrimaryButton(c, fmt.Sprintf(T("通过“%s”发送", "Send via “%s”"), env.InstanceName())).Disabled(!ready).Clicked() {
				if st.apiMethod == "GET" || st.apiMethod == "HEAD" {
					logsSendAPI(env, st, payload)
				} else {
					st.apiAsk = true
				}
			}
			if st.apiTask.Running {
				Busy(c, T("正在等待服务器…", "Waiting for the server…"))
			}
		})
		ErrorBanner(c, st.apiTask.Err)
		if st.apiAsk {
			msg := fmt.Sprintf(T("实例“%s”的服务器将发出 %s %s 请求。该请求可能修改上游数据，并且发出后不会自动重试。", "The server of “%s” will send %s %s. It may change data upstream, and is never retried automatically."),
				env.InstanceName(), st.apiMethod, strings.TrimSpace(st.apiURL))
			if ui.AlertDialog(c, &st.apiAsk, T("发送请求？", "Send the request?"), msg, T("取消", "Cancel"), T("发送", "Send")) == 1 {
				logsSendAPI(env, st, payload)
			}
		}
		if r := st.apiResult; r != nil && !st.apiTask.Running {
			col := t.Success
			if r.status >= 400 {
				col = t.Danger
			} else if r.status >= 300 {
				col = t.Warning
			}
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				Pill(c, fmt.Sprintf(T("上游返回 HTTP %d", "Upstream answered HTTP %d"), r.status), col)
				ui.Box(c).Grow(1)
				if PillButton(c, icCopy, T("复制结果", "Copy Result"), false).Clicked() {
					c.WriteClipboard(strings.Join(r.lines, "\n"))
					c.Toast(T("已复制", "Copied"))
				}
			})
			if r.note != "" {
				ui.Text(c, r.note).FontSize(12).TextColor(t.Warning)
			}
			if r.doc == nil {
				r.doc = newLogsDoc(r.lines)
			}
			logsMono(c, "logs-api-result", r.doc, 260, pal(t).Inset, false)
		}
	})
}

func logsOrDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

// logsCredOptions lists the credentials the call can use, with the labels
// that name them in the select; labels[0] is "none".
func logsCredOptions(creds []cpa.Credential) (indexes map[string]string, labels []string) {
	indexes = map[string]string{}
	none := logsNoCred()
	labels = []string{none}
	indexes[none] = ""
	for _, cr := range creds {
		if cr.AuthIndex == "" {
			continue
		}
		l := fmt.Sprintf("%s · %s · #%s", PrivName(cr.DisplayName()), providerLabel(cr.ProviderName()), cr.AuthIndex)
		labels = append(labels, l)
		indexes[l] = cr.AuthIndex
	}
	return indexes, labels
}

// logsParseHeaders reads "Name: value" lines.
func logsParseHeaders(s string) (map[string]string, error) {
	out := map[string]string{}
	for i, line := range strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		k, v, ok := strings.Cut(line, ":")
		k = strings.TrimSpace(k)
		if !ok || k == "" {
			return nil, fmt.Errorf(T("第 %d 行请求头应为“名称: 值”", "Header line %d should be “Name: value”"), i+1)
		}
		out[k] = strings.TrimSpace(v)
	}
	return out, nil
}

// logsBuildPayload is the body of POST /requests/api-call: method, url,
// header, data and the optional auth_index.
func logsBuildPayload(st *logsState, creds map[string]string, labels []string) (json.RawMessage, error) {
	u := strings.TrimSpace(st.apiURL)
	if u == "" {
		return nil, fmt.Errorf("%s", T("请填写 URL", "Enter a URL"))
	}
	if !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
		return nil, fmt.Errorf("%s", T("URL 须以 http:// 或 https:// 开头", "The URL must start with http:// or https://"))
	}
	h, err := logsParseHeaders(st.apiHeaders)
	if err != nil {
		return nil, err
	}
	m := map[string]any{"method": st.apiMethod, "url": u}
	if len(h) > 0 {
		m["header"] = h
	}
	if st.apiBody != "" {
		m["data"] = st.apiBody
	}
	if idx := creds[st.apiCred]; idx != "" {
		m["auth_index"] = idx
	}
	b, err := json.Marshal(m)
	return b, err
}

func logsSendAPI(env *PageEnv, st *logsState, payload json.RawMessage) {
	cl := env.Client
	if cl == nil {
		return
	}
	var out json.RawMessage
	Run(env.Win, &st.apiTask, 0, func(ctx context.Context) error {
		var err error
		out, err = cl.APICall(ctx, payload)
		return err
	}, func(err error) {
		if err != nil {
			return
		}
		st.apiResult = logsParseAPIResult(out)
	})
}

// logsParseAPIResult reads {status_code, header, body}, the server's
// account of what the upstream answered.
func logsParseAPIResult(raw json.RawMessage) *logsAPIResult {
	var r struct {
		StatusCode int                 `json:"status_code"`
		Header     map[string][]string `json:"header"`
		Body       string              `json:"body"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		lines, note := logsSplit(raw)
		return &logsAPIResult{lines: lines, note: note}
	}
	var lines []string
	keys := make([]string, 0, len(r.Header))
	for k := range r.Header {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		lines = append(lines, k+": "+strings.Join(r.Header[k], ", "))
	}
	if len(lines) > 0 {
		lines = append(lines, "")
	}
	body := r.Body
	var pretty bytes.Buffer
	if json.Indent(&pretty, []byte(body), "", "  ") == nil {
		body = pretty.String()
	}
	bl, note := logsSplit([]byte(body))
	return &logsAPIResult{status: r.StatusCode, lines: append(lines, bl...), note: note}
}

func logsCheckLatest(env *PageEnv, st *logsState) {
	cl := env.Client
	if cl == nil {
		return
	}
	running := env.Snap.Server.Version
	var raw json.RawMessage
	Run(env.Win, &st.latest, 0, func(ctx context.Context) error {
		var err error
		raw, err = cl.LatestVersion(ctx)
		return err
	}, func(err error) {
		if err != nil {
			st.latestText = ""
			return
		}
		st.latestText = logsLatestText(running, raw)
		st.latestTint = logsLatestTint(running, raw)
	})
}

// logsLatestText compares the running version with the server's answer
// {"latest-version": "v8.0.24"}.
func logsLatestText(running string, raw json.RawMessage) string {
	var v struct {
		Latest string `json:"latest-version"`
	}
	if json.Unmarshal(raw, &v) != nil || strings.TrimSpace(v.Latest) == "" {
		return T("服务器没有返回版本号", "The server returned no version")
	}
	switch cmp := logsCompareVersions(running, v.Latest); {
	case running == "":
		return fmt.Sprintf(T("最新版本 %s（未读到当前版本）", "Latest %s (the running version is unknown)"), v.Latest)
	case cmp < 0:
		return fmt.Sprintf(T("有新版本 %s（当前 %s）。CPA-Bar 不会替你升级服务器。", "New version %s available (running %s). CPA-Bar never upgrades the server."), v.Latest, running)
	case cmp == 0:
		return fmt.Sprintf(T("已是最新版本（%s）", "Up to date (%s)"), running)
	}
	return fmt.Sprintf(T("当前 %s 高于已发布的最新版 %s", "Running %s is newer than the latest release %s"), running, v.Latest)
}

// logsLatestTint colors the answer: green when up to date, orange when a
// newer release exists, gray otherwise.
func logsLatestTint(running string, raw json.RawMessage) Tint {
	var v struct {
		Latest string `json:"latest-version"`
	}
	if json.Unmarshal(raw, &v) != nil || strings.TrimSpace(v.Latest) == "" || running == "" {
		return TintGray
	}
	if logsCompareVersions(running, v.Latest) < 0 {
		return TintOrange
	}
	return TintGreen
}

// logsCompareVersions compares dotted versions ("v8.0.23" vs "8.0.24"):
// -1, 0 or 1. Parts that are not numbers compare as text.
func logsCompareVersions(a, b string) int {
	pa, pb := logsVersionParts(a), logsVersionParts(b)
	for i := 0; i < len(pa) || i < len(pb); i++ {
		var x, y string
		if i < len(pa) {
			x = pa[i]
		}
		if i < len(pb) {
			y = pb[i]
		}
		if x == "" {
			x = "0"
		}
		if y == "" {
			y = "0"
		}
		nx, ex := strconv.Atoi(x)
		ny, ey := strconv.Atoi(y)
		switch {
		case ex == nil && ey == nil:
			if nx != ny {
				return pick(nx < ny, -1, 1)
			}
		case x != y:
			return pick(x < y, -1, 1)
		}
	}
	return 0
}

func logsVersionParts(v string) []string {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i]
	}
	return strings.Split(v, ".")
}

// logsBytes formats a file size.
func logsBytes(n int64) string {
	switch {
	case n < 1<<10:
		return fmt.Sprintf("%d B", n)
	case n < 1<<20:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
}
