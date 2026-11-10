//go:build !darwin

package view

import (
	"errors"

	"github.com/egoist/mygo"
)

// setTrayContent: only the macOS menu bar has room for the quota styles.
func setTrayContent(*mygo.Tray, trayRender) error {
	return errors.New("the quota styles need the macOS menu bar")
}

func clearTrayContent() {}
