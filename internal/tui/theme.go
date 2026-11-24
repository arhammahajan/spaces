package tui

import (
	"github.com/charmbracelet/lipgloss"

	"github.com/arhammahajan/spaces/internal/model"
)

// Obsidian & muted slate-blue palette (matching kitty #0b0b0c so surfaces
// inherit Kitty's 0.68 frosted blur uniformly).
const (
	ColorCrust    = "#0b0b0c"
	ColorMantle   = "#0b0b0c"
	ColorBase     = "#0b0b0c"
	ColorSurface0 = "#2b323e"
	ColorSurface1 = "#3d4758"
	ColorOverlay0 = "#61768f"
	ColorSubtext0 = "#8c93a0"
	ColorText     = "#d8dadd"
	ColorLavender = "#b7d4ed"
	ColorBlue     = "#758a9b"
	ColorTeal     = "#88b8c8"
	ColorMauve    = "#9db4cc"
	ColorGreen    = "#98c379"
	ColorYellow   = "#e5c07b"
	ColorRed      = "#e06c75"
)

// StatusColor maps a pane/space status to its Catppuccin Mocha color.
func StatusColor(s model.Status) string {
	switch s {
	case model.StatusNeedsYou:
		return ColorBlue
	case model.StatusFailed:
		return ColorRed
	case model.StatusDone:
		return ColorGreen
	case model.StatusWorking:
		return ColorYellow
	case model.StatusQuiet, model.StatusNewOutput:
		return ColorBlue
	default:
		return ColorOverlay0
	}
}

// Shared Lipgloss styles.
var (
	styleBase      = lipgloss.NewStyle().Background(lipgloss.Color(ColorBase)).Foreground(lipgloss.Color(ColorText))
	styleHeader    = lipgloss.NewStyle().Background(lipgloss.Color(ColorMantle)).Foreground(lipgloss.Color(ColorText)).Bold(true)
	styleFooter    = lipgloss.NewStyle().Background(lipgloss.Color(ColorMantle)).Foreground(lipgloss.Color(ColorSubtext0))
	styleSelection = lipgloss.NewStyle().Background(lipgloss.Color(ColorSurface0)).Foreground(lipgloss.Color(ColorText))
	styleAccent    = lipgloss.NewStyle().Foreground(lipgloss.Color(ColorLavender)).Bold(true)
	styleMuted     = lipgloss.NewStyle().Foreground(lipgloss.Color(ColorOverlay0))
	styleSubtext   = lipgloss.NewStyle().Foreground(lipgloss.Color(ColorSubtext0))
	styleDivider   = lipgloss.NewStyle().Foreground(lipgloss.Color(ColorSurface0))
	styleError     = lipgloss.NewStyle().Foreground(lipgloss.Color(ColorRed)).Bold(true)
	styleOK        = lipgloss.NewStyle().Foreground(lipgloss.Color(ColorGreen)).Bold(true)
	styleBox       = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color(ColorSurface1)).Background(lipgloss.Color(ColorBase)).Foreground(lipgloss.Color(ColorText))
	styleDialogBox = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color(ColorLavender)).Background(lipgloss.Color(ColorBase)).Foreground(lipgloss.Color(ColorText)).Padding(0, 1)
)

func statusStyle(s model.Status) lipgloss.Style {
	return lipgloss.NewStyle().Foreground(lipgloss.Color(StatusColor(s))).Bold(true)
}
