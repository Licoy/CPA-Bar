//go:build !windows

package view

import "testing"

func TestPreferredSystemLocale(t *testing.T) {
	original := preferredLanguages
	t.Cleanup(func() { preferredLanguages = original })
	for _, languages := range [][]string{{"zh-Hans-CN", "en-GB"}, {"en-GB", "zh-Hans-CN"}, nil} {
		preferredLanguages = func() []string { return languages }
		got, err := preferredSystemLocale()
		if len(languages) == 0 {
			if err == nil {
				t.Error("missing system languages did not return an error")
			}
		} else if err != nil || got != languages[0] {
			t.Errorf("preferredSystemLocale() = %q, %v, want %q", got, err, languages[0])
		}
	}
}
