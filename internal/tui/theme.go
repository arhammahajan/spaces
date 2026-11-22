package tui

import (
	"github.com/charmbracelet/lipgloss"

	"github.com/arhammahajan/spaces/internal/model"
)

// Catppuccin Mocha palette, matching plugins/zellij-spaces/src/theme.rs.
const (
	ColorCrust    = "#11111b"
	ColorMantle   = "#181825"
	ColorBase     = "#1e1e2e"
	ColorSurface0 = "#313244"
	ColorSurface1 = "#45475a"
	ColorOverlay0 = "#6c7086"
	ColorSubtext0 = "#a6adc8"
	ColorText     = "#cdd6f4"
	ColorLavender = "#b4befe"
	ColorBlue     = "#89b4fa"
	ColorTeal     = "#94e2d5"
	ColorMauve    = "#cba6f7"
	ColorGreen    = "#a6e3a1"
	ColorYellow   = "#f9e2af"
	ColorRed      = "#f38ba8"
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
