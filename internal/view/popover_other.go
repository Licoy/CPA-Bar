//go:build !darwin

package view

type panelPopoverState struct{}

func panelOpacitySupported() bool           { return false }
func observePanelPopover(*App, bool) func() { return func() {} }
func (a *App) matchPanelAppearance()        {}
func panelContentInsets(*App) panelInsets   { return panelInsets{} }
func releasePanelPopover(a *App)            { a.panelSafeArea = panelInsets{} }
