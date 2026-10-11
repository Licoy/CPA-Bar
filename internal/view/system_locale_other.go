//go:build !windows

package view

import "github.com/egoist/mygo"

func preferredSystemLocale() (string, error) { return mygo.App.Locale(), nil }
