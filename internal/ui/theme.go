package ui

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Theme defines the named colors the TUI uses. The style block in model.go is
// rebuilt from a Theme via applyTheme.
type Theme struct {
	Name       string
	Label      string
	Phosphor   lipgloss.Color // primary accent (selection, brand, borders)
	PhosphorHi lipgloss.Color // brighter accent (brand letterforms)
	Amber      lipgloss.Color // peer / other-party highlight
	Cyan       lipgloss.Color // group member sender
	Cream      lipgloss.Color // body text
	Slate      lipgloss.Color // secondary body
	Muted      lipgloss.Color // meta / labels
	Subtle     lipgloss.Color // hairline chrome
	Hairline   lipgloss.Color // dividers
	Ink        lipgloss.Color // text on phosphor pill backgrounds
	Error      lipgloss.Color // alerts
}

var themes = []Theme{
	{
		Name: "phosphor", Label: "Phosphor",
		Phosphor: "42", PhosphorHi: "84", Amber: "215", Cyan: "117",
		Cream: "230", Slate: "250", Muted: "244", Subtle: "240",
		Hairline: "238", Ink: "16", Error: "203",
	},
	{
		Name: "sunset", Label: "Sunset",
		Phosphor: "208", PhosphorHi: "215", Amber: "173", Cyan: "110",
		Cream: "230", Slate: "250", Muted: "245", Subtle: "240",
		Hairline: "238", Ink: "16", Error: "197",
	},
	{
		Name: "ocean", Label: "Ocean",
		Phosphor: "44", PhosphorHi: "87", Amber: "175", Cyan: "117",
		Cream: "195", Slate: "152", Muted: "244", Subtle: "240",
		Hairline: "238", Ink: "16", Error: "203",
	},
	{
		Name: "plum", Label: "Plum",
		Phosphor: "135", PhosphorHi: "177", Amber: "215", Cyan: "117",
		Cream: "225", Slate: "250", Muted: "244", Subtle: "240",
		Hairline: "238", Ink: "16", Error: "203",
	},
	{
		Name: "forest", Label: "Forest",
		Phosphor: "29", PhosphorHi: "35", Amber: "137", Cyan: "108",
		Cream: "230", Slate: "250", Muted: "244", Subtle: "240",
		Hairline: "238", Ink: "16", Error: "124",
	},
	{
		Name: "paper", Label: "Paper",
		Phosphor: "255", PhosphorHi: "230", Amber: "187", Cyan: "152",
		Cream: "230", Slate: "250", Muted: "245", Subtle: "240",
		Hairline: "238", Ink: "232", Error: "174",
	},
}

// ThemeNames returns the ordered slug list of available themes.
func ThemeNames() []string {
	names := make([]string, 0, len(themes))
	for _, t := range themes {
		names = append(names, t.Name)
	}
	return names
}

// LookupTheme returns the theme matching slug (case-insensitive) and a flag
// indicating whether it was found.
func LookupTheme(slug string) (Theme, bool) {
	slug = strings.ToLower(strings.TrimSpace(slug))
	for _, t := range themes {
		if t.Name == slug {
			return t, true
		}
	}
	return Theme{}, false
}

// DefaultTheme returns the first registered theme (the canonical "Phosphor").
func DefaultTheme() Theme { return themes[0] }

// nextTheme returns the theme that follows the named one in registration
// order, wrapping around.
func nextTheme(current string) Theme {
	for i, t := range themes {
		if t.Name == current {
			return themes[(i+1)%len(themes)]
		}
	}
	return DefaultTheme()
}

func themeFilePath(dataDir string) string {
	if strings.TrimSpace(dataDir) == "" {
		return ""
	}
	return filepath.Join(dataDir, "theme")
}

// LoadPersistedThemeName reads the saved theme slug from dataDir; returns
// empty string when no file exists or dataDir is empty.
func LoadPersistedThemeName(dataDir string) string {
	path := themeFilePath(dataDir)
	if path == "" {
		return ""
	}
	data, err := os.ReadFile(path) // #nosec G304 -- constrained to configured data dir
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// SaveThemeName persists the theme slug to dataDir. Errors are silently
// swallowed; theme persistence is best-effort.
func SaveThemeName(dataDir, name string) {
	path := themeFilePath(dataDir)
	if path == "" {
		return
	}
	_ = os.WriteFile(path, []byte(name+"\n"), 0o600)
}

// applyTheme rebuilds the package-level styles from the supplied theme. All
// rendering code reads from the package-level style variables, so a single
// call here updates the entire TUI on the next render.
func applyTheme(t Theme) {
	currentTheme = t

	titleStyle = lipgloss.NewStyle().Bold(true).Foreground(t.Phosphor)
	brandStyle = lipgloss.NewStyle().Bold(true).Foreground(t.PhosphorHi)
	mutedStyle = lipgloss.NewStyle().Foreground(t.Muted)
	slateStyle = lipgloss.NewStyle().Foreground(t.Slate)
	subtleStyle = lipgloss.NewStyle().Foreground(t.Subtle)
	hairlineStyle = lipgloss.NewStyle().Foreground(t.Hairline)
	errorStyle = lipgloss.NewStyle().Bold(true).Foreground(t.Error)
	selectedItemStyle = lipgloss.NewStyle().Bold(true).Foreground(t.Phosphor)
	railStyle = lipgloss.NewStyle().Foreground(t.Phosphor)
	sectionStyle = lipgloss.NewStyle().Bold(true).Foreground(t.Phosphor)
	bodyStyle = lipgloss.NewStyle().Foreground(t.Cream)
	peerNameStyle = lipgloss.NewStyle().Bold(true).Foreground(t.Amber)
	youNameStyle = lipgloss.NewStyle().Bold(true).Foreground(t.Phosphor)
	memberNameStyle = lipgloss.NewStyle().Bold(true).Foreground(t.Cyan)
	mentionStyle = lipgloss.NewStyle().Bold(true).Foreground(t.Amber)
	monoStyle = lipgloss.NewStyle().Foreground(t.Cyan)
	// senderPalette gives each group member a stable color (picked by JID
	// hash). Theme accents first, then fixed 256-color fills chosen to stay
	// readable on dark backgrounds.
	senderPalette = []lipgloss.Style{
		lipgloss.NewStyle().Bold(true).Foreground(t.Cyan),
		lipgloss.NewStyle().Bold(true).Foreground(t.Amber),
		lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("141")),
		lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("84")),
		lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("210")),
		lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("117")),
		lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("179")),
		lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("111")),
	}
	timestampStyle = lipgloss.NewStyle().Foreground(t.Subtle)
	receiptReadStyle = lipgloss.NewStyle().Foreground(t.Phosphor)
	receiptDeliveredStyle = lipgloss.NewStyle().Foreground(t.Slate)
	receiptSentStyle = lipgloss.NewStyle().Foreground(t.Muted)
	unreadPillStyle = lipgloss.NewStyle().Bold(true).Foreground(t.Phosphor)
	chipKeyStyle = lipgloss.NewStyle().Bold(true).Foreground(t.Phosphor)
	chipLabelStyle = lipgloss.NewStyle().Foreground(t.Muted)
	statusDotOnStyle = lipgloss.NewStyle().Bold(true).Foreground(t.Phosphor)
	statusDotWarnStyle = lipgloss.NewStyle().Bold(true).Foreground(t.Amber)
	statusDotErrStyle = lipgloss.NewStyle().Bold(true).Foreground(t.Error)
	toolbarButtonStyle = lipgloss.NewStyle().Bold(true).Foreground(t.Cream).Background(t.Hairline).Padding(0, 1)
	toolbarActiveButtonStyle = lipgloss.NewStyle().Bold(true).Foreground(t.Ink).Background(t.Phosphor).Padding(0, 1)
	boxStyle = lipgloss.NewStyle().Border(lipgloss.ThickBorder()).BorderForeground(t.Phosphor).Padding(1, 2)
	boxMutedStyle = lipgloss.NewStyle().Border(lipgloss.NormalBorder()).BorderForeground(t.Subtle).Padding(0, 2)
	qrBoxStyle = lipgloss.NewStyle().Border(lipgloss.NormalBorder()).BorderForeground(t.Phosphor).Padding(1, 2)
}
