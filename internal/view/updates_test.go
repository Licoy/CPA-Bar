package view

import (
	"testing"

	"github.com/licoy/cpa-bar/internal/store"
)

func TestUpdaterLanguageMatchesActiveLanguage(t *testing.T) {
	defer func() { _ = SetLanguage(store.LanguageZH) }()
	for _, test := range []struct {
		language string
		want     string
	}{
		{store.LanguageZH, "zh-Hans"},
		{store.LanguageEN, "en"},
	} {
		if err := SetLanguage(test.language); err != nil {
			t.Fatal(err)
		}
		if got := updaterLanguage(); got != test.want {
			t.Errorf("updaterLanguage() = %q, want %q", got, test.want)
		}
	}
}
