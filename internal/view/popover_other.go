//go:build !darwin

package view

import "github.com/egoist/mygo"

// matchAppAppearance is for macOS's popover; elsewhere the panel is a
// window of the app's.
func matchAppAppearance(*mygo.Window) {}
