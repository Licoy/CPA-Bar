//go:build !windows

package view

import (
	"errors"

	"github.com/egoist/mygo"
)

var preferredLanguages = mygo.App.PreferredLanguages

func preferredSystemLocale() (string, error) {
	languages := preferredLanguages()
	if len(languages) == 0 {
		return "", errors.New("read system preferred languages: no languages returned")
	}
	return languages[0], nil
}
