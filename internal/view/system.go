package view

import (
	"github.com/egoist/mygo"
	"github.com/egoist/mygo/plugins/updater"
)

// system holds the calls into the running app that views make while they
// build; they need App.Run, so tests replace them.
var system = struct {
	OpenAtLogin            func() bool
	SetOpenAtLogin         func(bool) error
	NotificationsSupported func() bool
	UpdatesEnabled         func() bool
	AutomaticUpdateChecks  func() bool
	SetAutomaticChecks     func(bool)
	CheckForUpdates        func()
	SetThemeSource         func(mygo.ThemeSource)
	Locale                 func() (string, error)
}{
	OpenAtLogin:            func() bool { return mygo.App.OpenAtLogin() },
	SetOpenAtLogin:         func(on bool) error { return mygo.App.SetOpenAtLogin(on) },
	NotificationsSupported: mygo.NotificationsSupported,
	UpdatesEnabled:         func() bool { return mygo.Updater.Enabled() },
	AutomaticUpdateChecks:  updater.AutomaticChecks,
	SetAutomaticChecks:     updater.SetAutomaticChecks,
	CheckForUpdates:        updater.CheckForUpdates,
	SetThemeSource:         mygo.Theme.SetSource,
	Locale:                 preferredSystemLocale,
}
