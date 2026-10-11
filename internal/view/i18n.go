package view

import (
	"sync/atomic"

	"github.com/licoy/cpa-bar/internal/store"
)

// lang is the interface language; it changes from the settings page, on
// the main thread, and is read by views.
var lang atomic.Value

func init() { lang.Store(store.LanguageZH) }

// SetLanguage switches the interface language.
func SetLanguage(l string) {
	if l != store.LanguageEN {
		l = store.LanguageZH
	}
	lang.Store(l)
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
