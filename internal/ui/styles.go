package ui

import (
	"image/color"
	"strings"

	"charm.land/bubbles/v2/textinput"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/hdweiss/coralctl/internal/theme"
)

// Colors and styles of the UI, set by applyTheme.
var (
	colAccent, colFg, colMuted, colBorder, colSelBg, colSelBgLo, colBarBg color.Color
	colGreen, colYellow, colRed, colBlue, colCyan, colPurple, colOrange   color.Color

	stMuted, stAccent, stBold, stHeader, stErr, stWarn, stKey, stString, stNumber, stBool lipgloss.Style
	stSel, stSelLo, stBar, stBarKey, stBarText, stLogo, stLink                            lipgloss.Style
)

func init() { applyTheme(theme.Default()) }

// applyTheme sets the colors and styles of the UI. Views build their output
// from these on every render, so the next frame shows the new theme.
func applyTheme(t theme.Theme) {
	c := lipgloss.Color
	colAccent, colFg, colMuted, colBorder = c(t.Accent), c(t.Fg), c(t.Muted), c(t.Border)
	colSelBg, colSelBgLo, colBarBg = c(t.SelBg), c(t.SelBgLo), c(t.BarBg)
	colGreen, colYellow, colRed, colBlue = c(t.Green), c(t.Yellow), c(t.Red), c(t.Blue)
	colCyan, colPurple, colOrange = c(t.Cyan), c(t.Purple), c(t.Orange)

	stMuted = lipgloss.NewStyle().Foreground(colMuted)
	stAccent = lipgloss.NewStyle().Foreground(colAccent)
	stBold = lipgloss.NewStyle().Bold(true)
	stHeader = lipgloss.NewStyle().Foreground(colMuted).Bold(true)
	stErr = lipgloss.NewStyle().Foreground(colRed)
	stWarn = lipgloss.NewStyle().Foreground(colYellow)
	stKey = lipgloss.NewStyle().Foreground(colCyan)
	stString = lipgloss.NewStyle().Foreground(colGreen)
	stNumber = lipgloss.NewStyle().Foreground(colOrange)
	stBool = lipgloss.NewStyle().Foreground(colPurple)
	stSel = lipgloss.NewStyle().Background(colSelBg).Foreground(colFg).Bold(true)
	stSelLo = lipgloss.NewStyle().Background(colSelBgLo).Foreground(colFg)
	stBar = lipgloss.NewStyle().Background(colBarBg).Foreground(colFg)
	stBarKey = lipgloss.NewStyle().Background(colBarBg).Foreground(colAccent).Bold(true)
	stBarText = lipgloss.NewStyle().Background(colBarBg).Foreground(colMuted)
	stLogo = lipgloss.NewStyle().Background(colAccent).Foreground(c(t.LogoFg)).Bold(true)
	stLink = lipgloss.NewStyle().Foreground(colBlue).Underline(true)
}

// inputStyles styles a text input in the theme colors with the given prompt.
func inputStyles(prompt lipgloss.Style) textinput.Styles {
	st := textinput.DefaultDarkStyles()
	st.Focused.Prompt = prompt
	st.Focused.Text = lipgloss.NewStyle().Foreground(colFg)
	st.Focused.Placeholder = stMuted
	st.Cursor.Color = colFg
	return st
}

// statusStyle colors well-known status values.
func statusStyle(s string) lipgloss.Style {
	switch s {
	case "Running", "Ready", "Active", "Bound", "Complete", "True":
		return lipgloss.NewStyle().Foreground(colGreen)
	case "Completed", "Succeeded":
		return stMuted
	case "Pending", "ContainerCreating", "Terminating", "PodInitializing":
		return lipgloss.NewStyle().Foreground(colYellow)
	case "":
		return lipgloss.NewStyle()
	}
	if strings.Contains(s, "Err") || strings.Contains(s, "BackOff") || strings.HasPrefix(s, "NotReady") ||
		s == "Failed" || s == "Lost" || s == "OOMKilled" || s == "Unknown" || s == "Evicted" {
		return lipgloss.NewStyle().Foreground(colRed)
	}
	return lipgloss.NewStyle()
}

// fit truncates or pads s (which may contain ANSI codes) to exactly w cells.
func fit(s string, w int) string {
	if w <= 0 {
		return ""
	}
	sw := ansi.StringWidth(s)
	if sw > w {
		return ansi.Truncate(s, w, "…")
	}
	return s + strings.Repeat(" ", w-sw)
}

// rect is a screen region in absolute cell coordinates.
type rect struct{ x, y, w, h int }

func (r rect) contains(x, y int) bool {
	return x >= r.x && x < r.x+r.w && y >= r.y && y < r.y+r.h
}

// inner is the content area inside a one-cell border.
func (r rect) inner() rect { return rect{r.x + 1, r.y + 1, r.w - 2, r.h - 2} }

// frame draws a rounded box of size w×h with a title in the top border and an
// optional footer in the bottom border. lines are fitted to the inner width.
func frame(title, footer string, lines []string, w, h int, focused bool) string {
	if w < 4 || h < 2 {
		return strings.Repeat(strings.Repeat(" ", max(w, 0))+"\n", max(h-1, 0)) + strings.Repeat(" ", max(w, 0))
	}
	bc := colBorder
	ts := stMuted.Bold(true)
	if focused {
		bc = colAccent
		ts = stAccent.Bold(true)
	}
	b := lipgloss.NewStyle().Foreground(bc)
	iw := w - 2

	var sb strings.Builder
	t := ""
	if title != "" {
		t = " " + ts.Render(ansi.Truncate(title, iw-4, "…")) + " "
	}
	sb.WriteString(b.Render("╭─") + t + b.Render(strings.Repeat("─", max(iw-1-ansi.StringWidth(t), 0))+"╮"))
	for i := 0; i < h-2; i++ {
		line := ""
		if i < len(lines) {
			line = lines[i]
		}
		sb.WriteString("\n" + b.Render("│") + fit(line, iw) + b.Render("│"))
	}
	f := ""
	if footer != "" {
		f = " " + stMuted.Render(ansi.Truncate(footer, iw-4, "…")) + " "
	}
	sb.WriteString("\n" + b.Render("╰"+strings.Repeat("─", max(iw-1-ansi.StringWidth(f), 0))) + f + b.Render("─╯"))
	return sb.String()
}

// scrollTo adjusts offset so that cursor is visible within height rows.
func scrollTo(cursor, offset, height int) int {
	if height <= 0 {
		return 0
	}
	if cursor < offset {
		return cursor
	}
	if cursor >= offset+height {
		return cursor - height + 1
	}
	return offset
}

func clamp(v, lo, hi int) int {
	if hi < lo {
		return lo
	}
	return min(max(v, lo), hi)
}
