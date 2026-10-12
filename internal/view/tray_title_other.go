//go:build !darwin

package view

import (
	"strings"

	"github.com/egoist/mygo"
)

// setTrayLines shows lines as the title of the tray item; only macOS has
// titles, on one line.
func setTrayLines(tray *mygo.Tray, lines []string) { tray.SetTitle(strings.Join(lines, " · ")) }
