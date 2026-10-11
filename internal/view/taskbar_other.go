//go:build !windows

package view

// taskbarLight reports whether the bar the tray icon sits on is light.
// The panels of Linux desktops are mostly dark, and macOS tints the
// template icon itself.
func taskbarLight() bool { return false }
