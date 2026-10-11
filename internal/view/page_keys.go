package view

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"
)

// Client keys are the v8 configuration's access.api-keys: a plain list of
// strings that clients send as their bearer token. (The v8 top-level
// api-keys is a map of upstream provider groups; see page_providers.go.)
// A key has no flags of its own, so there is nothing to enable or disable:
// it is in the list or it is not.

func init() {
	RegisterPage(Page{
		ID:       "keys",
		Order:    11,
		Group:    GroupManage,
		Title:    func() string { return T("客户端密钥", "Client Keys") },
		Instance: true,
		View:     keysPage,
	})
}

var clientKeysPath = []string{"access", "api-keys"}

type keysState struct {
	started bool
	loaded  bool
	keys    []string
	load    Task
	op      Task

	reveal   map[string]bool
	newKey   string
	showNew  bool
	remove   string
	rmOpen   bool
	conflict string
}

func newKeysState() *keysState { return &keysState{reveal: map[string]bool{}} }

const base62 = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

// generateClientKey makes "sk-" and 32 random base62 characters.
func generateClientKey() (string, error) {
	out := make([]byte, 0, 32)
	buf := make([]byte, 64)
	for len(out) < 32 {
		if _, err := rand.Read(buf); err != nil {
			return "", err
		}
		for _, b := range buf {
			// 248 = 4 * 62: reject the rest so no character is likelier.
			if b < 248 && len(out) < 32 {
				out = append(out, base62[b%62])
			}
		}
	}
	return "sk-" + string(out), nil
}

func decodeKeyList(raw json.RawMessage) ([]string, error) {
	if len(raw) == 0 || strings.TrimSpace(string(raw)) == "null" {
		return []string{}, nil
	}
	var list []string
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, fmt.Errorf("%s", T("无法解析客户端密钥列表", "Could not parse the client key list"))
	}
	return list, nil
}

func (s *keysState) reload(env *PageEnv) {
	if env.Client == nil {
		return
	}
	s.started = true
	cl := env.Client
	var list []string
	env.Run(&s.load, func(ctx context.Context) error {
		raw, err := cfgGet(ctx, cl, clientKeysPath...)
		if err != nil {
			return err
		}
		list, err = decodeKeyList(raw)
		return err
	}, func(err error) {
		if err == nil {
			s.keys, s.loaded = list, true
		}
	})
}

// mutate changes the list from a fresh copy, unless the server's list is no
// longer the one shown; the whole list is sent, as lists replace.
func (s *keysState) mutate(env *PageEnv, apply func(fresh []string) ([]string, error), onDone func()) {
	cl := env.Client
	shown := slices.Clone(s.keys)
	var latest []string
	env.Run(&s.op, func(ctx context.Context) error {
		raw, err := cfgGet(ctx, cl, clientKeysPath...)
		if err != nil {
			return err
		}
		fresh, err := decodeKeyList(raw)
		if err != nil {
			return err
		}
		if !slices.Equal(fresh, shown) {
			latest = fresh
			return &cfgChanged{}
		}
		next, err := apply(fresh)
		if err != nil {
			return err
		}
		body, _ := json.Marshal(append([]string{}, next...))
		return cl.PutConfig(ctx, body, clientKeysPath...)
	}, func(err error) {
		var ch *cfgChanged
		switch {
		case errors.As(err, &ch):
			s.keys = latest
			s.conflict = ch.Error()
			s.op.Err = nil
		case err == nil:
			s.conflict = ""
			onDone()
			s.reload(env)
		case IsIndeterminate(err):
			s.reload(env)
		}
	})
}

func keysPage(c *ui.Context, env *PageEnv) {
	t := c.Theme()
	st := State(env, "keys", newKeysState)
	PageHeader(c, T("客户端密钥", "Client Keys"), T("调用此实例代理接口时使用的密钥（access.api-keys），不是上游提供商的密钥。", "The keys clients use to call this instance's proxy API (access.api-keys); not the upstream providers' keys."), func() {
		if BusyPillButton(c, icRefresh, T("重新加载", "Reload"), st.load.Running, env.Client == nil || st.op.Running).Clicked() {
			st.reload(env)
		}
	})
	if env.Client == nil {
		return
	}
	if !st.started {
		st.reload(env)
	}
	if st.load.Running && !st.loaded {
		Busy(c, T("正在读取密钥…", "Reading keys…"))
	}
	if !st.load.Running {
		ErrorBanner(c, st.load.Err)
	}
	warnBanner(c, st.conflict)
	ErrorBanner(c, st.op.Err)
	if !st.loaded {
		return
	}

	Section(c, T("添加密钥", "Add a key"), func() {
		Card(c).Children(func() {
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				in := ui.TextInput(c.Key("new-key"), &st.newKey).Label(T("新密钥", "New key")).Grow(1).Placeholder("sk-…")
				if !st.showNew {
					in.Password()
				}
				if PillButton(c, icRefresh, T("生成随机密钥", "Generate"), false).Clicked() {
					if k, err := generateClientKey(); err == nil {
						st.newKey, st.showNew = k, true
					} else {
						st.op.Err = err
					}
				}
			})
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Checkbox(c.Key("show-new"), &st.showNew, T("显示", "Show"))
				ui.Box(c).Grow(1)
				ui.Text(c, T("目标实例：", "Target: ")+env.InstanceName()).FontSize(12).TextColor(t.TextMuted)
				key := strings.TrimSpace(st.newKey)
				if ui.PrimaryButton(c, T("添加", "Add")).Disabled(key == "" || st.op.Running || st.load.Running).Clicked() {
					if slices.Contains(st.keys, key) {
						st.op.Err = fmt.Errorf("%s", T("该密钥已存在", "This key already exists"))
					} else {
						st.mutate(env, func(fresh []string) ([]string, error) {
							return append(slices.Clone(fresh), key), nil
						}, func() { st.newKey, st.showNew = "", false })
					}
				}
			})
			ui.Text(c, T("生成后请先复制保存：添加后列表中默认只显示首尾各 4 位。", "Copy a generated key before adding it: the list shows only its first and last 4 characters.")).FontSize(11).TextColor(t.TextMuted)
		})
	})

	Section(c, fmt.Sprintf(T("现有密钥（%d）", "Current keys (%d)"), len(st.keys)), func() {
		if len(st.keys) == 0 {
			Card(c).Children(func() {
				ui.Text(c, T("没有客户端密钥。", "No client keys.")).FontSize(12).TextColor(t.TextMuted)
			})
		}
		if len(st.keys) > 0 {
			Card(c).Padding(4, 0).Gap(0).Children(func() {
				for i, k := range st.keys {
					if i > 0 {
						ui.Box(c).Height(1).Margin(0, 14).Background(pal(t).Line)
					}
					ui.Row(c).Key(i).Gap(10).Padding(10, 14).AlignItems(ui.Center).Children(func() {
						Tile(c, icKey, TintBlue, 32, false)
						text := cfgMask(k)
						if st.reveal[k] {
							text = k
						}
						ui.Text(c, text).Grow(1).Basis(0).Font("monospace").FontSize(13).SingleLine().Selectable()
						show, icon := T("显示", "Show"), cfgIcEye
						if st.reveal[k] {
							show, icon = T("隐藏", "Hide"), cfgIcEyeOff
						}
						if PillButton(c.Key("show-"+strconv.Itoa(i)), icon, show, false).Clicked() {
							st.reveal[k] = !st.reveal[k]
						}
						if PillButton(c.Key("copy-"+strconv.Itoa(i)), icCopy, T("复制", "Copy"), false).Clicked() {
							c.WriteClipboard(k)
							c.Toast(T("已复制密钥", "Key copied"))
						}
						if cfgDangerPill(c.Key("del-"+strconv.Itoa(i)), icTrash, T("删除", "Delete"), st.op.Running).Clicked() {
							st.remove, st.rmOpen = k, true
						}
					})
				}
			})
		}
	})
	if st.op.Running {
		Busy(c, T("正在保存…", "Saving…"))
	} else if st.op.Err == nil && st.op.Done && c.Now().Sub(st.op.At) < 4e9 {
		ui.Text(c, T("已保存", "Saved")).FontSize(12).TextColor(t.Success)
		c.After(4e9)
	}

	if st.rmOpen {
		msg := fmt.Sprintf(T("将从实例“%s”删除客户端密钥 %s，使用它的客户端会立即无法访问。", "Client key %[2]s will be removed from instance “%[1]s”; clients using it lose access at once."), env.InstanceName(), cfgMask(st.remove))
		if len(st.keys) == 1 {
			msg += "\n" + T("这是最后一个密钥，删除后实例将没有任何客户端密钥。", "This is the last key: the instance will have no client keys.")
		}
		if ui.AlertDialog(c, &st.rmOpen, T("删除客户端密钥？", "Delete client key?"), msg, T("取消", "Cancel"), T("确认删除", "Confirm Delete")) == 1 {
			victim := st.remove
			st.mutate(env, func(fresh []string) ([]string, error) {
				if !slices.Contains(fresh, victim) {
					return nil, fmt.Errorf("%s", T("该密钥已不在服务器上", "The key is no longer on the server"))
				}
				return slices.DeleteFunc(slices.Clone(fresh), func(s string) bool { return s == victim }), nil
			}, func() { delete(st.reveal, victim) })
			st.remove = ""
		}
	}
}
