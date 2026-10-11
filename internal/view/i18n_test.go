package view

import (
	"errors"
	"testing"

	"github.com/licoy/cpa-bar/internal/store"
)

func TestLanguageForLocale(t *testing.T) {
	for _, test := range []struct {
		setting string
		locale  string
		want    string
	}{
		{store.LanguageAuto, "zh", store.LanguageZH},
		{store.LanguageAuto, "zh-CN", store.LanguageZH},
		{store.LanguageAuto, "ZH_tw", store.LanguageZH},
		{store.LanguageAuto, "zh-Hant-HK", store.LanguageZH},
		{store.LanguageAuto, "en-US", store.LanguageEN},
		{store.LanguageAuto, "ja-JP", store.LanguageEN},
		{store.LanguageZH, "en-US", store.LanguageZH},
		{store.LanguageEN, "zh-CN", store.LanguageEN},
	} {
		if got := languageForLocale(test.setting, test.locale); got != test.want {
			t.Errorf("languageForLocale(%q, %q) = %q, want %q", test.setting, test.locale, got, test.want)
		}
	}
}

func TestSetLanguageAutoUsesSystemLocale(t *testing.T) {
	original := system.Locale
	defer func() { system.Locale = original; _ = SetLanguage(store.LanguageZH) }()

	system.Locale = func() (string, error) { return "en-GB", nil }
	if err := SetLanguage(store.LanguageAuto); err != nil {
		t.Fatal(err)
	}
	if !English() {
		t.Error("automatic language did not follow an English system locale")
	}

	system.Locale = func() (string, error) { return "", errors.New("locale unavailable") }
	if err := SetLanguage(store.LanguageAuto); err == nil {
		t.Error("automatic language hid the system locale error")
	}
	if !English() {
		t.Error("failed automatic switch changed the active language")
	}
	if err := SetLanguage(store.LanguageZH); err != nil || English() {
		t.Errorf("explicit Chinese depends on the system locale: err=%v, English=%v", err, English())
	}
}
