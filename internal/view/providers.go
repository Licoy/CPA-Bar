package view

import (
	"embed"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"
	"github.com/licoy/cpa-bar/internal/cpa"
)

// providerNames are the display names of the providers CPA knows, keyed by
// the lower-case id it reports.
var providerNames = map[string]string{
	"claude":               "Claude (Anthropic)",
	"anthropic":            "Claude (Anthropic)",
	"codex":                "Codex (OpenAI)",
	"openai":               "OpenAI",
	"antigravity":          "Antigravity",
	"kimi":                 "Kimi",
	"kimi-ai":              "Kimi AI",
	"xai":                  "xAI (Grok)",
	"grok":                 "xAI (Grok)",
	"devin":                "Devin",
	"meta":                 "Meta",
	"gemini":               "Gemini",
	"gemini-cli":           "Gemini CLI",
	"vertex":               "Vertex AI",
	"aistudio":             "AI Studio",
	"qwen":                 "Qwen",
	"iflow":                "iFlow",
	"openai-compatibility": "OpenAI Compatible",
}

// providerKey is the form of a provider id the names are keyed by.
func providerKey(id string) string {
	k := strings.ToLower(strings.TrimSpace(id))
	k = strings.ReplaceAll(k, "_", "-")
	if k == "x-ai" {
		k = "xai"
	}
	return k
}

// providerLabel is how a provider id is shown: "claude" → "Claude
// (Anthropic)". An unknown id (a plugin's, say) has each word capitalised:
// "my-llm" → "My Llm" stays recognisable as its id.
func providerLabel(id string) string {
	k := providerKey(id)
	if k == "" {
		return ""
	}
	if n, ok := providerNames[k]; ok {
		return n
	}
	// CPA names an OpenAI-compatible upstream after its configured name.
	if name, ok := strings.CutPrefix(k, "openai-compatible-"); ok && name != "" {
		return providerNames["openai-compatibility"] + " · " + name
	}
	words := strings.FieldsFunc(strings.TrimSpace(id), func(r rune) bool { return r == '-' || r == '_' || r == ' ' })
	for i, w := range words {
		r, n := utf8.DecodeRuneInString(w)
		words[i] = string(unicode.ToUpper(r)) + w[n:]
	}
	return strings.Join(words, " ")
}

// The providers' logos are LobeHub Icons' (https://icons.lobehub.com, MIT),
// copied by scripts/gen-glyphs.py. See AGENTS.md.
//
//go:embed assets/providers/*.svg
var providerSVGs embed.FS

// providerLogoFiles names the logo of each provider id, by its file in
// assets/providers. An OpenAI-compatible upstream has OpenAI's.
var providerLogoFiles = map[string]string{
	"claude":               "claude-color",
	"anthropic":            "claude-color",
	"codex":                "codex-color",
	"openai":               "openai",
	"antigravity":          "antigravity-color",
	"kimi":                 "kimi",
	"kimi-ai":              "kimi",
	"xai":                  "grok",
	"grok":                 "grok",
	"devin":                "devin-color",
	"meta":                 "meta-color",
	"gemini":               "gemini-color",
	"gemini-cli":           "geminicli-color",
	"vertex":               "vertexai-color",
	"aistudio":             "aistudio",
	"qwen":                 "qwen-color",
	"openai-compatibility": "openai",
}

var providerLogos = map[string]*ui.SVG{}

func init() {
	for _, name := range providerLogoFiles {
		if providerLogos[name] != nil {
			continue
		}
		data, err := providerSVGs.ReadFile("assets/providers/" + name + ".svg")
		if err != nil {
			panic(err)
		}
		providerLogos[name] = ui.MustParseSVG(data)
	}
}

// providerLogo is the logo of a provider id, or nil for one LobeHub has
// none of (iFlow, a plugin's).
func providerLogo(id string) *ui.SVG {
	k := providerKey(id)
	if strings.HasPrefix(k, "openai-compatible-") {
		k = "openai-compatibility"
	}
	return providerLogos[providerLogoFiles[k]]
}

// ProviderIcon is a provider's logo, size points square: in its colors, or
// in the text color for a one-color logo. A provider without one has a
// plug.
func ProviderIcon(c *ui.Context, id string, size float32) ui.Element {
	if logo := providerLogo(id); logo != nil {
		return ui.Image(c, logo).Size(size, size)
	}
	return ui.Icon(c, icPlug).Size(size, size).TextColor(c.Theme().TextMuted)
}

// providerTile is a provider's logo on a tile.
func providerTile(c *ui.Context, id string, size float32) ui.Element {
	return ui.Row(c).Size(size, size).Radius(size*0.28).Background(pal(c.Theme()).Inset).
		Border(1, pal(c.Theme()).Line).Justify(ui.Center).AlignItems(ui.Center).Children(func() {
		ProviderIcon(c, id, size*0.6)
	})
}

// credTile is an account's tile: its provider's logo, with a dot in the
// color of its health at the corner, ringed in ring, the color the tile
// sits on.
func credTile(c *ui.Context, cr cpa.Credential, size float32, ring ui.Color) ui.Element {
	h := cr.Health()
	dot := max(8, size*0.32)
	return ui.Column(c).Size(size, size).Children(func() {
		providerTile(c, cr.ProviderName(), size)
		ui.Box(c).Absolute().Right(-2).Bottom(-2).Size(dot, dot).Radius(dot/2).
			Background(HealthColor(c.Theme(), h)).Border(2, ring).
			Label(HealthLabel(h))
	})
}

// ProviderPanelTitle heads a panel about one provider, as PanelTitle does
// with its logo for the tile.
func ProviderPanelTitle(c *ui.Context, id, title, trailing string) {
	t := c.Theme()
	ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
		providerTile(c, id, 30)
		ui.Text(c, title).FontSize(17).FontWeight(600).SingleLine().Grow(1)
		if trailing != "" {
			ui.Text(c, trailing).FontSize(13).TextColor(t.TextMuted).SingleLine()
		}
	})
}
