package view

import (
	"errors"
	"fmt"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/licoy/cpa-bar/internal/core"
	"github.com/licoy/cpa-bar/internal/cpa"
	"github.com/licoy/cpa-bar/internal/store"
)

// ConnLabel names a connection state. States are always spelled out, never
// shown by color alone.
func ConnLabel(s core.ConnState) string {
	switch s {
	case core.ConnOnline:
		return T("在线", "Online")
	case core.ConnOffline:
		return T("离线", "Offline")
	case core.ConnAuth:
		return T("鉴权失败", "Auth failed")
	case core.ConnUnavailable:
		return T("管理接口不可用", "Management API unavailable")
	case core.ConnError:
		return T("异常", "Error")
	case core.ConnPaused:
		return T("已暂停", "Paused")
	}
	return T("检查中", "Checking")
}

// ConnColor is the accent of a connection state.
func ConnColor(t *ui.Theme, s core.ConnState) ui.Color {
	switch s {
	case core.ConnOnline:
		return t.Success
	case core.ConnOffline, core.ConnAuth, core.ConnError:
		return t.Danger
	case core.ConnUnavailable:
		return t.Warning
	}
	return t.TextMuted
}

// HealthLabel names a credential's run state.
func HealthLabel(h cpa.Health) string {
	switch h {
	case cpa.HealthAvailable:
		return T("可用", "Available")
	case cpa.HealthCooling:
		return T("冷却中", "Cooling down")
	case cpa.HealthError:
		return T("异常", "Error")
	case cpa.HealthDisabled:
		return T("已停用", "Disabled")
	case cpa.HealthPending:
		return T("处理中", "Pending")
	}
	return ""
}

// HealthColor is the accent of a credential's run state.
func HealthColor(t *ui.Theme, h cpa.Health) ui.Color {
	switch h {
	case cpa.HealthAvailable:
		return t.Success
	case cpa.HealthCooling:
		return t.Warning
	case cpa.HealthError:
		return t.Danger
	}
	return t.TextMuted
}

// StatusDot draws a small dot of a status color, beside a text that names
// the status.
func StatusDot(c *ui.Context, col ui.Color) ui.Element {
	return ui.Box(c).Size(8, 8).Radius(4).Background(col)
}

// Ago formats a past time relatively: "刚刚", "3 分钟前".
func Ago(t time.Time, now time.Time) string {
	if t.IsZero() {
		return T("从未", "never")
	}
	d := now.Sub(t)
	switch {
	case d < 10*time.Second:
		return T("刚刚", "just now")
	case d < time.Minute:
		return fmt.Sprintf(T("%d 秒前", "%ds ago"), int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf(T("%d 分钟前", "%dm ago"), int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf(T("%d 小时前", "%dh ago"), int(d.Hours()))
	}
	return t.Local().Format("2006-01-02 15:04")
}

// Until formats a future time: "2 分钟后".
func Until(t time.Time, now time.Time) string {
	d := t.Sub(now)
	switch {
	case d <= 0:
		return T("即将", "soon")
	case d < time.Minute:
		return fmt.Sprintf(T("%d 秒后", "in %ds"), int(d.Seconds())+1)
	case d < time.Hour:
		return fmt.Sprintf(T("%d 分钟后", "in %dm"), int(d.Minutes())+1)
	}
	return t.Local().Format("01-02 15:04")
}

// ErrorText explains an error to the user, with the server's own message
// kept.
func ErrorText(err error) string { return PrivText(errorText(err)) }

func errorText(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, store.ErrSecretNotFound) {
		return T("未保存管理密钥，请在实例设置中填写", "No management key stored; set it in the instance settings")
	}
	var ae *cpa.APIError
	if !errors.As(err, &ae) {
		return err.Error()
	}
	var head string
	switch ae.Kind {
	case cpa.KindNetwork:
		head = T("无法连接服务器", "Cannot reach the server")
	case cpa.KindTimeout:
		head = T("请求超时", "Request timed out")
	case cpa.KindIndeterminate:
		head = T("结果待确认：请求已发出但未收到结果，请刷新后确认，不要重复提交", "Result unknown: the request was sent but no answer came back; refresh to confirm before trying again")
	case cpa.KindUnauthorized:
		head = T("管理密钥缺失或错误，已暂停自动刷新", "The management key is missing or wrong; automatic refresh paused")
	case cpa.KindForbidden:
		head = T("拒绝访问：可能未开启远程管理、未设置管理密钥或 IP 已被临时封禁，已暂停自动刷新", "Access denied: remote management may be off, no key set, or this IP banned; automatic refresh paused")
	case cpa.KindManagementUnavailable:
		head = T("管理接口不可用：可能是 CPA 版本过旧（需 v8）、未启用管理功能或地址错误", "Management API unavailable: CPA may be older than v8, management may be disabled, or the address is wrong")
	case cpa.KindNotFound:
		head = T("目标不存在", "Not found")
	case cpa.KindRateLimited:
		head = T("请求过于频繁", "Rate limited")
	case cpa.KindInvalid:
		head = T("服务端拒绝了请求", "The server rejected the request")
	case cpa.KindConflict:
		head = T("操作冲突或部分失败", "Conflict or partial failure")
	case cpa.KindServer:
		head = T("服务端错误", "Server error")
	case cpa.KindDecode:
		head = T("无法解析服务端响应", "Could not parse the server's response")
	case cpa.KindCanceled:
		head = T("已取消", "Canceled")
	default:
		head = T("请求失败", "Request failed")
	}
	detail := ae.Message
	if ae.Status != 0 {
		detail = fmt.Sprintf("HTTP %d %s", ae.Status, detail)
	}
	if detail == "" || ae.Kind == cpa.KindCanceled {
		return head
	}
	return head + "（" + detail + "）"
}

// IsIndeterminate reports a write whose outcome is unknown.
func IsIndeterminate(err error) bool { return cpa.KindOf(err) == cpa.KindIndeterminate }
