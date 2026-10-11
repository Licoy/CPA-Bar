//go:build windows

package view

import (
	"fmt"

	"golang.org/x/sys/windows"
)

func preferredSystemLocale() (string, error) {
	languages, err := windows.GetUserPreferredUILanguages(windows.MUI_LANGUAGE_NAME)
	if err != nil {
		return "", fmt.Errorf("read Windows preferred UI languages: %w", err)
	}
	if len(languages) == 0 {
		return "", fmt.Errorf("read Windows preferred UI languages: no languages returned")
	}
	return languages[0], nil
}
