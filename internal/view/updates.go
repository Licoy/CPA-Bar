package view

import (
	"runtime"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/plugins/updater"
	"github.com/egoist/mygo/plugins/updater/native"
	"github.com/licoy/cpa-bar/internal/store"
)

// UpdateInterval is the time between background update checks.
const UpdateInterval = time.Hour

// UseUpdater installs the native update window: it checks GitHub
// releases in the background, shows the notes, and offers to install, to
// be reminded later or to skip the version; it verifies the update's
// signature before installing and relaunches only when the user agrees.
// Call after SetLanguage succeeds and before App.Run.
func UseUpdater(st store.Settings) error {
	if err := configureUpdaterHTTP(); err != nil {
		return err
	}
	mygo.Use(native.New(updater.Options{
		Interval:               UpdateInterval,
		DisableAutomaticChecks: !st.AutoCheckUpdates,
		Language:               updaterLanguage(),
	}))
	return nil
}

func updaterLanguage() string {
	if English() {
		return "en"
	}
	return "zh-Hans"
}

// CheckForUpdates checks now, as the user asked: the window says when the
// app is up to date or why the check failed.
func CheckForUpdates() { system.CheckForUpdates() }

// UpdatesEnabled reports whether this build can update itself: development
// builds, packages of a package manager (.deb) and read-only install
// locations cannot.
func UpdatesEnabled() bool { return system.UpdatesEnabled() }

// AutomaticUpdateChecks reports whether background checks are on; the
// updater keeps this choice itself, as its window also offers it.
func AutomaticUpdateChecks() bool { return system.AutomaticUpdateChecks() }

// SetAutomaticUpdateChecks turns background checks on or off.
func SetAutomaticUpdateChecks(on bool) { system.SetAutomaticChecks(on) }

// mygoTrayTitleSupported reports whether the tray can show text next to
// its icon: only the macOS menu bar can.
func mygoTrayTitleSupported() bool { return runtime.GOOS == "darwin" }
