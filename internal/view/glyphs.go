package view

import "github.com/egoist/mygo/ui"

// The interface icons are Remix Icon's (https://remixicon.com, Apache 2.0),
// written to glyphs_gen.go by scripts/gen-glyphs.py, and drawn in the text
// color. See AGENTS.md.

func remix(body string) *ui.SVG {
	return ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="currentColor">` + body + `</svg>`))
}
