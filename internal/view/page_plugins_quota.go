package view

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/licoy/cpa-bar/internal/cpa"
)

// The quota a plugin reports for the credentials of its provider: GET
// /plugins/{id}/quota?auth_index=N asks the plugin's quota provider about
// that credential, so the dialog first has the user pick one.

// pluginsQuota is the state of the quota dialog.
type pluginsQuota struct {
	open     bool
	id, name string
	// provider is the provider the plugin declares for its quota; empty when
	// it declares none, which offers every credential that has a quota.
	provider string
	// cred is the label of the selected credential.
	cred string

	task Task
	// shownFor names the credential the result below is for.
	shownFor string
	text     string
	lines    []string
	note     string
	list     ui.ListState
}

// pluginHasQuota reports that the plugin declares a quota provider.
func pluginHasQuota(p cpa.Plugin) bool {
	return p.SupportsQuota || strings.TrimSpace(p.QuotaProvider) != ""
}

// pluginsQuotaCreds are the credentials a plugin's quota can be read for:
// those of the plugin's provider (by the credential's quota provider, or
// by its provider), or, when the plugin names none, any credential with a
// quota. Credentials without an auth_index cannot be asked about.
func pluginsQuotaCreds(provider string, creds []cpa.Credential) []cpa.Credential {
	provider = strings.TrimSpace(provider)
	var out []cpa.Credential
	for _, cr := range creds {
		if cr.AuthIndex == "" {
			continue
		}
		switch {
		case provider == "":
			if !cr.SupportsQuota && cr.QuotaProvider == "" {
				continue
			}
		case !strings.EqualFold(cr.QuotaProvider, provider) && !strings.EqualFold(cr.Provider, provider):
			continue
		}
		out = append(out, cr)
	}
	return out
}

func pluginsQuotaLabel(cr cpa.Credential) string {
	return fmt.Sprintf("%s · %s · #%s", PrivName(cr.DisplayName()), providerLabel(cr.ProviderName()), cr.AuthIndex)
}

// pluginsOpenQuota opens the dialog; with a single credential to ask about
// it asks at once.
func pluginsOpenQuota(env *PageEnv, st *pluginsState, p cpa.Plugin, meta pluginsMeta) {
	st.quota = pluginsQuota{open: true, id: p.ID, name: pluginsMetaName(meta, p.ID), provider: p.QuotaProvider}
	if cands := pluginsQuotaCreds(p.QuotaProvider, env.Snap.Credentials); len(cands) == 1 {
		st.quota.cred = pluginsQuotaLabel(cands[0])
		pluginsFetchQuota(env, st, cands[0])
	}
}

func pluginsFetchQuota(env *PageEnv, st *pluginsState, cr cpa.Credential) {
	cl := env.Client
	if cl == nil {
		return
	}
	q := &st.quota
	id, idx, label := q.id, cr.AuthIndex, pluginsQuotaLabel(cr)
	var raw json.RawMessage
	Run(env.Win, &q.task, 0, func(ctx context.Context) error {
		var err error
		raw, err = cl.PluginQuota(ctx, id, idx)
		return err
	}, func(err error) {
		if err != nil || st.quota.id != id {
			return
		}
		text := pluginsPrettyJSON(raw)
		lines, note := logsSplit([]byte(text))
		st.quota.shownFor, st.quota.text, st.quota.lines, st.quota.note = label, text, lines, note
		st.quota.list = ui.ListState{}
	})
}

// pluginsPrettyJSON indents a JSON answer; anything else is shown as is.
func pluginsPrettyJSON(raw json.RawMessage) string {
	var b bytes.Buffer
	if json.Indent(&b, raw, "", "  ") == nil {
		return b.String()
	}
	return strings.TrimSpace(string(raw))
}

func pluginsQuotaDialog(c *ui.Context, env *PageEnv, st *pluginsState) {
	q := &st.quota
	if !q.open {
		return
	}
	t := c.Theme()
	cands := pluginsQuotaCreds(q.provider, env.Snap.Credentials)
	labels := make([]string, len(cands))
	for i, cr := range cands {
		labels[i] = pluginsQuotaLabel(cr)
	}
	if len(labels) > 0 && !pluginsHas(labels, q.cred) {
		q.cred = labels[0]
	}
	ui.Modal(c, &q.open, func() {
		ui.Column(c).Width(560).Padding(20).Gap(12).Children(func() {
			ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
				Tile(c, icGauge, TintPurple, 30, false)
				ui.Text(c, fmt.Sprintf(T("%s 的额度", "Quota of %s"), q.name)).FontSize(17).FontWeight(600).SingleLine().Grow(1).Basis(0)
			})
			sub := fmt.Sprintf(T("实例“%s”上的插件 %s", "Plugin %s on “%s”"), env.InstanceName(), q.id)
			if prov := strings.TrimSpace(q.provider); prov != "" {
				sub += " · " + prov
			}
			ui.Text(c, sub).FontSize(12).TextColor(t.TextMuted).MaxLines(2)

			if len(cands) == 0 {
				Notice(c, T("此实例没有可查询额度的账号：需要有属于该插件额度提供商的账号。", "This instance has no account this plugin can report a quota for: it needs an account of the plugin's quota provider."))
			} else {
				ui.Form(c, func() {
					ui.Field(c, T("账号", "Account"), func() {
						ui.Select(c.Key("plugin-quota-cred"), &q.cred, labels)
					})
				})
				ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
					idx := pluginsIndexOf(labels, q.cred)
					if ui.PrimaryButton(c, T("获取额度", "Get Quota")).Disabled(env.Client == nil || q.task.Running || idx < 0).Clicked() {
						pluginsFetchQuota(env, st, cands[idx])
					}
					if q.task.Running {
						Busy(c, T("正在向插件查询…", "Asking the plugin…"))
					}
				})
			}
			ErrorBanner(c, q.task.Err)

			if len(q.lines) > 0 && !q.task.Running {
				ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
					ui.Text(c, q.shownFor).FontSize(12).TextColor(t.TextMuted).SingleLine().Grow(1).Basis(0)
					if PillButton(c, icCopy, T("复制额度", "Copy Quota"), false).Clicked() {
						c.WriteClipboard(q.text)
						c.Toast(T("已复制", "Copied"))
					}
				})
				if q.note != "" {
					ui.Text(c, q.note).FontSize(12).TextColor(t.Warning)
				}
				h := float32(min(len(q.lines), 18))*17 + 16
				ui.Column(c).Height(h).Padding(8, 4).Radius(12).Background(pal(t).Card).Children(func() {
					ui.List(c.Key("plugin-quota-result"), &q.list, len(q.lines), func(i int) {
						ui.Text(c, q.lines[i]).FontSize(12).Font("monospace").Selectable().Padding(0, 10)
					}).Grow(1)
				})
			}
			ui.Row(c).Children(func() {
				ui.Box(c).Grow(1)
				if ui.Button(c, T("关闭", "Close")).Clicked() {
					q.open = false
				}
			})
		})
	})
}

func pluginsHas(list []string, s string) bool { return pluginsIndexOf(list, s) >= 0 }

func pluginsIndexOf(list []string, s string) int {
	for i, v := range list {
		if v == s {
			return i
		}
	}
	return -1
}
