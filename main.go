// CPA-Bar watches and manages CLIProxyAPI (CPA) servers from the menu bar
// or the system tray.
package main

import (
	_ "embed"
	"encoding/json"
	"log"
	"os"
	"path/filepath"

	"github.com/egoist/mygo"
	"github.com/licoy/cpa-bar/internal/core"
	"github.com/licoy/cpa-bar/internal/store"
	"github.com/licoy/cpa-bar/internal/usage"
	"github.com/licoy/cpa-bar/internal/view"
)

// mygoJSON is the packaging config, which holds the app's version.
//
//go:embed mygo.json
var mygoJSON []byte

// appVersion is the version of the packaged app, or, run with go run or
// go build, the version in mygo.json.
func appVersion() string {
	if v := mygo.App.Version(); v != "" {
		return v
	}
	var cfg struct {
		Version string `json:"version"`
	}
	if json.Unmarshal(mygoJSON, &cfg) != nil {
		return ""
	}
	return cfg.Version
}

func main() {
	app := mygo.App
	app.SetName("CPA-Bar")
	if !app.RequestSingleInstanceLock() {
		// The running instance opens its window.
		return
	}
	if v := appVersion(); v != "" {
		view.Version = v
	}

	svc, err := newService()
	if err != nil {
		// Never start over a settings file that cannot be read: saving
		// would replace it. Say what is wrong and quit.
		log.Println("settings:", err)
		app.WhenReady(func() {
			mygo.Dialog.Error("CPA-Bar", view.T("无法读取设置文件，应用未启动以免覆盖它：", "Cannot read the settings file; the app did not start, so as not to overwrite it: ")+"\n\n"+err.Error())
			app.Quit()
		})
		if err := app.Run(); err != nil {
			log.Fatal(err)
		}
		os.Exit(1)
	}

	st := svc.Settings()
	if err := view.SetLanguage(st.Language); err != nil {
		log.Fatal("language: ", err)
	}
	view.ApplyAppearance(st.Appearance)
	view.UseUpdater(st)

	ui := view.NewApp(svc)
	notify = ui.Notify
	// Usage records live next to the settings, one file per instance.
	if dir, err := mygo.App.Path(mygo.PathUserData); err == nil {
		ui.Usage = usage.New(usage.Options{Dir: filepath.Join(dir, "usage")})
	}
	app.OnSecondInstance(func([]string, string) { ui.OpenManager("") })
	// A tray app keeps running with no window open; on macOS it has no Dock
	// icon until the management window opens.
	app.SetActivationPolicy(mygo.ActivationPolicyAccessory)
	app.OnWindowAllClosed(func() {})
	app.OnWillQuit(func(*mygo.QuitEvent) {
		svc.Close()
		if ui.Usage != nil {
			ui.Usage.Close()
		}
	})
	app.WhenReady(ui.Ready)
	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
}

// notify delivers service events to the interface once it exists.
var notify = func(core.Event) {}

func newService() (*core.Service, error) {
	dir, err := mygo.App.Path(mygo.PathUserData)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return core.New(core.Options{
		Settings: store.File{Path: filepath.Join(dir, store.FileName)},
		Secrets:  store.Keyring{},
		OnEvent:  func(e core.Event) { notify(e) },
	})
}
