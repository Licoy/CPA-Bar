package view

import (
	"strings"
	"sync/atomic"

	"github.com/licoy/cpa-bar/internal/store"
)

// lang is the interface language; it changes from the settings page, on
// the main thread, and is read by views.
var lang atomic.Value

func init() { lang.Store(store.LanguageZH) }

// SetLanguage switches the interface language. Auto follows the user's
// preferred system language.
func SetLanguage(l string) error {
	resolved, err := resolveLanguage(l)
	if err != nil {
		return err
	}
	setResolvedLanguage(resolved)
	return nil
}

func resolveLanguage(l string) (string, error) {
	locale := ""
	if l != store.LanguageEN && l != store.LanguageZH {
		var err error
		locale, err = system.Locale()
		if err != nil {
			return "", err
		}
	}
	return languageForLocale(l, locale), nil
}

func setResolvedLanguage(l string) { lang.Store(l) }

func languageForLocale(setting, locale string) string {
	if setting == store.LanguageEN || setting == store.LanguageZH {
		return setting
	}
	parts := strings.FieldsFunc(strings.TrimSpace(locale), func(r rune) bool {
		return r == '-' || r == '_' || r == '.' || r == '@'
	})
	if len(parts) > 0 && strings.EqualFold(parts[0], "zh") {
		return store.LanguageZH
	}
	return store.LanguageEN
}

// English reports whether the interface is in English.
func English() bool { return lang.Load() == store.LanguageEN }

// T picks the text of the current language. Texts live beside the code
// that shows them, in both languages, so neither can be forgotten.
func T(zh, en string) string {
	if English() {
		return en
	}
	return zh
}
