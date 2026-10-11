package view

import (
	"context"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/licoy/cpa-bar/internal/cpa"
)

func init() {
	RegisterPage(Page{
		ID:       "oauth",
		Order:    1,
		Group:    GroupManage,
		Title:    func() string { return T("OAuth 登录", "OAuth Login") },
		Instance: true,
		View:     oauthPage,
	})
}

const (
	// oauthPollEvery is how often a waiting login is asked about.
	oauthPollEvery = 2 * time.Second
	// oauthPollFailures is how many failed polls in a row end the wait.
	oauthPollFailures = 10
)

// Where a login stands.
const (
	loginWaiting = iota
	loginDone
	loginFailed
	loginExpired
	loginCanceled
)

// oauthSession is the login of this window, started on the instance the
// state belongs to.
type oauthSession struct {
	provider  string
	info      cpa.OAuthStart
	startedAt time.Time
	status    int
	// errText is why the login failed.
	errText string
	// pollErr is the last failed poll of a login still waiting.
	pollErr   error
	pollFails int
	// submitted: a callback URL was handed to the server.
	submitted bool
}

type oauthState struct {
	provider string // chosen built-in provider, by display name
	custom   string // plugin provider id; wins when filled
	callback string // pasted callback URL

	start, cancel, poll, submit Task
	lastPoll                    time.Time
	sess                        *oauthSession
}

func newOAuthState() *oauthState { return &oauthState{} }

func oauthProviderLabel(id string) string { return providerLabel(id) }

// oauthProviderChip is a built-in provider to pick, with its logo; it
// reports a click.
func oauthProviderChip(c *ui.Context, id, label string, on, disabled bool) bool {
	t := c.Theme()
	bg, line := pal(t).Inset, pal(t).Line
	if on {
		bg, line = t.Accent.Alpha(0.12), t.Accent
	}
	b := ui.ButtonBase(c).FocusRing(true).Label(label).Selected(on).Disabled(disabled).
		Padding(6, 12, 6, 8).Radius(999).Background(bg).Border(1, line)
	b.Gap(6).AlignItems(ui.Center).Children(func() {
		ProviderIcon(c, id, 16)
		ui.Text(c, label).FontSize(13).FontWeight(pick(on, 600, 400)).TextColor(pick(on, t.Accent, t.Text)).SingleLine()
	})
	return b.Clicked()
}

// effectiveProvider is the provider id a login would start for.
func (s *oauthState) effectiveProvider() string {
	if p := strings.ToLower(strings.TrimSpace(s.custom)); p != "" {
		return p
	}
	for _, id := range cpa.OAuthProviders {
		if oauthProviderLabel(id) == s.provider {
			return id
		}
	}
	return ""
}

func oauthPage(c *ui.Context, env *PageEnv) {
	t := c.Theme()
	st := State(env, "oauth", newOAuthState)
	canWrite := env.Client != nil
	instance := env.InstanceName()
	sess := st.sess
	waiting := sess != nil && sess.status == loginWaiting

	PageHeader(c, T("OAuth 登录", "OAuth Login"), fmt.Sprintf(T("目标实例：%s", "Target instance: %s"), instance), nil)
	Notice(c, fmt.Sprintf(T("登录在你本机的浏览器中完成，登录成功后凭证由服务器“%s”保存，CPA-Bar 不接触令牌。", "The login happens in your own browser; once it succeeds the server “%s” stores the credential. CPA-Bar never handles the tokens."), instance))

	// Provider picker.
	labels := make([]string, 0, len(cpa.OAuthProviders))
	for _, id := range cpa.OAuthProviders {
		labels = append(labels, oauthProviderLabel(id))
	}
	if !slices.Contains(labels, st.provider) && len(labels) > 0 {
		st.provider = labels[0]
	}
	Panel(c).Children(func() {
		PanelTitle(c, icLogin, TintPurple, T("选择服务商", "Choose a provider"), "→ "+instance, false)
		ui.Column(c).Gap(6).Children(func() {
			ui.Text(c, T("内置服务商", "Built-in provider")).FontSize(12).TextColor(t.TextMuted)
			ui.Row(c).Wrap().Gap(8).Children(func() {
				custom := strings.TrimSpace(st.custom) != ""
				for _, id := range cpa.OAuthProviders {
					label := oauthProviderLabel(id)
					if oauthProviderChip(c.Key("provider-"+id), id, label, st.provider == label && !custom, waiting) {
						st.provider = label
						st.custom = ""
					}
				}
			})
		})
		ui.Column(c).Gap(4).Children(func() {
			ui.Text(c, T("插件服务商 ID（可选，填写后优先使用）", "Plugin provider ID (optional; used when filled)")).FontSize(12).TextColor(t.TextMuted)
			ui.TextInput(c.Key("custom"), &st.custom).Placeholder(T("例如 my-plugin", "e.g. my-plugin")).Disabled(waiting).Label(T("插件服务商 ID", "Plugin provider ID"))
		})
		provider := st.effectiveProvider()
		ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
			label := T("开始", "Start")
			if provider != "" {
				label = fmt.Sprintf(T("开始登录 %s", "Start login: %s"), provider)
			}
			if ui.PrimaryButton(c.Key("start"), label).Radius(999).Disabled(!canWrite || waiting || st.start.Running || provider == "").
				Tooltip(fmt.Sprintf(T("在“%s”上开始登录", "Start a login on “%s”"), instance)).Clicked() {
				startOAuth(env, st, provider)
			}
			if st.start.Running {
				Busy(c, T("正在向服务器申请登录…", "Asking the server for a login…"))
			}
		})
		ErrorBanner(c, st.start.Err)
	})

	if sess == nil {
		return
	}
	oauthDrive(c, env, st, sess)
	oauthSessionView(c, env, st, sess)
}

// oauthDrive keeps a waiting login polled while the page is built; leaving
// the page or the instance builds no frames, so the polling stops.
func oauthDrive(c *ui.Context, env *PageEnv, st *oauthState, sess *oauthSession) {
	if sess.status != loginWaiting {
		return
	}
	now := c.Now()
	if sess.info.ExpiresIn > 0 && !now.Before(sess.expiresAt()) {
		sess.status = loginExpired
		return
	}
	c.After(time.Second)
	if st.poll.Running || st.cancel.Running || st.start.Running || env.Client == nil {
		return
	}
	if now.Sub(st.lastPoll) >= oauthPollEvery {
		st.lastPoll = now
		pollOAuth(env, st, sess)
	}
}

func (s *oauthSession) expiresAt() time.Time {
	return s.startedAt.Add(time.Duration(s.info.ExpiresIn) * time.Second)
}

func oauthSessionView(c *ui.Context, env *PageEnv, st *oauthState, sess *oauthSession) {
	t := c.Theme()
	instance := env.InstanceName()
	canWrite := env.Client != nil

	Panel(c).Children(func() {
		PanelTitle(c, icKey, TintBlue, T("当前登录", "Current login"), oauthProviderLabel(sess.provider)+" → "+instance, false)

		ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
			switch sess.status {
			case loginWaiting:
				Pill(c, T("进行中", "In progress"), t.Accent)
				Busy(c, T("等待你在浏览器中完成登录…", "Waiting for you to finish in the browser…"))
				if sess.info.ExpiresIn > 0 {
					left := max(sess.expiresAt().Sub(c.Now()), 0)
					ui.Text(c, fmt.Sprintf(T("剩余时间 %s", "Time left %s"), oauthClock(left))).FontSize(12).TextColor(t.TextMuted).FontFeatures("tnum")
				}
			case loginDone:
				Pill(c, T("已完成", "Done"), t.Success)
				ui.Text(c, fmt.Sprintf(T("登录成功，凭证已保存到实例“%s”。", "Logged in; the credential is stored on “%s”."), instance)).FontSize(13).FontWeight(600).TextColor(t.Success)
			case loginFailed:
				Pill(c, T("失败", "Failed"), t.Danger)
				ui.Text(c, T("登录失败", "Login failed")).FontSize(13).FontWeight(600).TextColor(t.Danger)
			case loginExpired:
				Pill(c, T("已超时", "Timed out"), t.Warning)
				ui.Text(c, T("登录已超时，请重新开始。", "The login timed out; start again.")).FontSize(13).FontWeight(600).TextColor(t.Warning)
			case loginCanceled:
				Pill(c, T("已取消", "Canceled"), t.TextMuted)
				ui.Text(c, T("登录已取消。", "The login was canceled.")).FontSize(13).TextColor(t.TextMuted)
			}
		})
		if sess.status == loginFailed && sess.errText != "" {
			ui.Text(c, sess.errText).FontSize(12).Selectable().MaxLines(4)
		}
		if sess.status == loginWaiting && sess.pollErr != nil {
			ui.Text(c, T("暂时无法读取登录状态：", "Cannot read the login status right now: ")+ErrorText(sess.pollErr)).FontSize(11).TextColor(t.Warning).MaxLines(3).Selectable()
		}

		if sess.status == loginWaiting {
			oauthInstructions(c, sess)
			if sess.info.Flow != "device" {
				oauthCallbackSection(c, env, st, sess)
			}
		}

		ErrorBanner(c, st.cancel.Err)
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			if sess.status == loginWaiting {
				if PillButton(c.Key("cancel"), icX, T("取消登录", "Cancel Login"), !canWrite || st.cancel.Running).
					Tooltip(fmt.Sprintf(T("让“%s”放弃这次登录", "Ask “%s” to drop this login"), instance)).Clicked() {
					cancelOAuth(env, st, sess)
				}
				if st.cancel.Running {
					Busy(c, T("正在取消…", "Canceling…"))
				}
				return
			}
			if sess.status == loginDone {
				if ui.PrimaryButton(c.Key("accounts"), T("查看账号", "View Accounts")).Radius(999).Clicked() {
					env.App.page = "accounts"
				}
			}
			if PillButton(c.Key("close"), nil, pick(sess.status == loginDone, T("继续登录其他账号", "Log In Another"), T("关闭", "Close")), false).Clicked() {
				st.sess = nil
				st.callback = ""
				st.cancel.Err = nil
			}
		})
	})
}

// oauthStep is one step of a login: a numbered card with its state.
func oauthStep(c *ui.Context, n int, title, state string, col ui.Color, body func()) {
	t := c.Theme()
	ui.Column(c.Key(n)).Padding(12, 14).Gap(10).Radius(12).Background(pal(t).Inset).Children(func() {
		ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
			ui.Row(c).Size(22, 22).Radius(11).Background(t.Accent.Alpha(0.15)).Justify(ui.Center).AlignItems(ui.Center).Children(func() {
				ui.Text(c, fmt.Sprint(n)).FontSize(12).FontWeight(600).TextColor(t.Accent)
			})
			ui.Text(c, title).FontSize(13).FontWeight(600).Grow(1).SingleLine()
			if state != "" {
				Pill(c, state, col)
			}
		})
		body()
	})
}

// oauthInstructions shows the link to open, or the device code to enter.
func oauthInstructions(c *ui.Context, sess *oauthSession) {
	t := c.Theme()
	info := sess.info
	n := 1
	if info.UserCode != "" || info.Flow == "device" {
		oauthStep(c, n, T("输入验证码", "Enter the code"), T("进行中", "In progress"), t.Accent, func() {
			ui.Text(c, T("在打开的页面中输入此验证码：", "Enter this code on the page that opens:")).FontSize(12).TextColor(t.TextMuted)
			if info.UserCode != "" {
				ui.Row(c).Gap(12).AlignItems(ui.Center).Children(func() {
					ui.Text(c, info.UserCode).FontSize(34).FontWeight(700).Font("monospace").Selectable()
					if PillButton(c.Key("copy-code"), icCopy, T("复制验证码", "Copy Code"), false).Clicked() {
						c.WriteClipboard(info.UserCode)
						c.Toast(T("已复制验证码", "Code copied"))
					}
				})
			}
		})
		n++
		if info.URL == "" {
			return
		}
		oauthStep(c, n, T("打开验证页面", "Open the page"), "", t.Accent, func() { oauthLink(c, info.URL) })
		return
	}
	if info.URL == "" {
		return
	}
	oauthStep(c, n, T("在浏览器中登录", "Log in with the browser"), T("进行中", "In progress"), t.Accent, func() {
		ui.Text(c, T("在浏览器中打开下面的链接并登录：", "Open this link in your browser and log in:")).FontSize(12).TextColor(t.TextMuted)
		oauthLink(c, info.URL)
	})
}

// oauthLink shows a login link with the buttons to open and copy it.
func oauthLink(c *ui.Context, link string) {
	ui.Text(c, link).FontSize(11).Font("monospace").Selectable().MaxLines(4)
	ui.Row(c).Gap(8).Children(func() {
		if PillButton(c.Key("open"), icExternal, T("在浏览器中打开", "Open in Browser"), !safeWebURL(link)).Clicked() {
			c.OpenURL(link)
		}
		if PillButton(c.Key("copy-url"), icCopy, T("复制链接", "Copy Link"), false).Clicked() {
			c.WriteClipboard(link)
			c.Toast(T("已复制链接", "Link copied"))
		}
	})
}

// oauthCallbackSection takes the address the browser ended up on, for a
// server the browser cannot be redirected to.
func oauthCallbackSection(c *ui.Context, env *PageEnv, st *oauthState, sess *oauthSession) {
	t := c.Theme()
	instance := env.InstanceName()
	state, col := T("可选", "Optional"), t.TextMuted
	if sess.submitted {
		state, col = T("已提交", "Sent"), t.Success
	}
	oauthStep(c, 2, T("提交回调 URL", "Submit callback URL"), state, col, func() {
		ui.Text(c, T("如果登录后浏览器停在一个打不开的页面（例如服务器在远程，回调地址是 localhost），请复制地址栏里的完整网址，粘贴到这里提交。", "If the browser ends on a page that does not open (for example the server is remote and the callback is localhost), copy the full address from the address bar and paste it here.")).
			FontSize(12).TextColor(t.TextMuted)
		ui.TextInput(c.Key("callback"), &st.callback).Placeholder("http://localhost:1455/auth/callback?code=…&state=…").Label(T("回调 URL", "Callback URL"))
		valid := validCallbackURL(st.callback)
		if strings.TrimSpace(st.callback) != "" && !valid {
			ui.Text(c, T("这不像完整的回调网址（需要 http(s):// 开头并带有查询参数）。", "This does not look like a full callback URL (it needs http(s):// and query parameters).")).FontSize(11).TextColor(t.Warning)
		}
		if sess.submitted {
			ui.Text(c, T("已提交，等待服务器完成登录…", "Submitted; waiting for the server to finish the login…")).FontSize(12).TextColor(t.Success)
		}
		ErrorBanner(c, st.submit.Err)
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			if PillButton(c.Key("submit"), nil, T("提交", "Submit"), env.Client == nil || !valid || st.submit.Running).
				Tooltip(fmt.Sprintf(T("把回调网址提交给“%s”", "Hand the callback URL to “%s”"), instance)).Clicked() {
				submitCallback(env, st, sess)
			}
			if st.submit.Running {
				Busy(c, T("正在提交…", "Submitting…"))
			}
		})
	})
}

// ---- Operations ----

func startOAuth(env *PageEnv, st *oauthState, provider string) {
	client := env.Client
	var out *cpa.OAuthStart
	Run(env.Win, &st.start, 0, func(ctx context.Context) error {
		var err error
		out, err = client.StartOAuth(ctx, provider, nil)
		return err
	}, func(err error) {
		if err != nil || out == nil {
			return
		}
		now := time.Now()
		st.sess = &oauthSession{provider: provider, info: *out, startedAt: now, status: loginWaiting}
		st.callback = ""
		st.submit.Err, st.cancel.Err = nil, nil
		st.lastPoll = now
	})
}

func pollOAuth(env *PageEnv, st *oauthState, sess *oauthSession) {
	client, svc, id := env.Client, env.App.Svc, env.InstanceID
	state := sess.info.State
	var out *cpa.OAuthStatus
	Run(env.Win, &st.poll, 15*time.Second, func(ctx context.Context) error {
		var err error
		out, err = client.OAuthStatus(ctx, state)
		return err
	}, func(err error) {
		if st.sess != sess || sess.status != loginWaiting {
			return
		}
		sess.pollErr = err
		if err != nil {
			sess.pollFails++
			switch cpa.KindOf(err) {
			case cpa.KindUnauthorized, cpa.KindForbidden, cpa.KindManagementUnavailable:
				sess.status, sess.errText = loginFailed, ErrorText(err)
			default:
				if sess.pollFails >= oauthPollFailures {
					sess.status, sess.errText = loginFailed, ErrorText(err)
				}
			}
			return
		}
		sess.pollFails = 0
		switch out.Status {
		case cpa.OAuthOK:
			sess.status = loginDone
			svc.Refresh(id)
		case cpa.OAuthError:
			sess.status = loginFailed
			sess.errText = out.Error
			if sess.errText == "" {
				sess.errText = T("服务器报告登录失败", "The server reported that the login failed")
			}
		}
	})
}

func cancelOAuth(env *PageEnv, st *oauthState, sess *oauthSession) {
	client := env.Client
	state := sess.info.State
	Run(env.Win, &st.cancel, 0, func(ctx context.Context) error {
		return client.CancelOAuth(ctx, state)
	}, func(err error) {
		// An unknown outcome leaves the login waiting: polling shows
		// what became of it.
		if err == nil && st.sess == sess && sess.status == loginWaiting {
			sess.status = loginCanceled
		}
	})
}

func submitCallback(env *PageEnv, st *oauthState, sess *oauthSession) {
	client := env.Client
	provider, raw := sess.provider, strings.TrimSpace(st.callback)
	Run(env.Win, &st.submit, 0, func(ctx context.Context) error {
		return client.SubmitOAuthCallback(ctx, provider, raw)
	}, func(err error) {
		if err == nil && st.sess == sess {
			sess.submitted = true
			st.callback = ""
		}
	})
}

// ---- Helpers ----

// safeWebURL reports an http or https link: the only kind a server's reply
// may open in the browser.
func safeWebURL(s string) bool {
	u, err := url.Parse(strings.TrimSpace(s))
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

// validCallbackURL reports a full redirect URL: web scheme, host, and the
// query (or fragment) the provider appended.
func validCallbackURL(s string) bool {
	u, err := url.Parse(strings.TrimSpace(s))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return false
	}
	return u.RawQuery != "" || u.Fragment != ""
}

// clock formats a duration as m:ss.
func oauthClock(d time.Duration) string {
	s := int(d.Round(time.Second).Seconds())
	return fmt.Sprintf("%d:%02d", s/60, s%60)
}
