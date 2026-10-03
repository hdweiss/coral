package ui

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

var (
	colAccent  = lipgloss.Color("#FF7F50") // coral
	colFg      = lipgloss.Color("#D8DEE9")
	colMuted   = lipgloss.Color("#7B8494")
	colBorder  = lipgloss.Color("#3B4252")
	colSelBg   = lipgloss.Color("#3B4252")
	colSelBgLo = lipgloss.Color("#2A303B")
	colBarBg   = lipgloss.Color("#232831")
	colGreen   = lipgloss.Color("#A3BE8C")
	colYellow  = lipgloss.Color("#EBCB8B")
	colRed     = lipgloss.Color("#BF616A")
	colBlue    = lipgloss.Color("#81A1C1")
	colCyan    = lipgloss.Color("#88C0D0")
	colPurple  = lipgloss.Color("#B48EAD")
	colOrange  = lipgloss.Color("#D08770")
)

var (
	stMuted   = lipgloss.NewStyle().Foreground(colMuted)
	stAccent  = lipgloss.NewStyle().Foreground(colAccent)
	stBold    = lipgloss.NewStyle().Bold(true)
	stHeader  = lipgloss.NewStyle().Foreground(colMuted).Bold(true)
	stErr     = lipgloss.NewStyle().Foreground(colRed)
	stWarn    = lipgloss.NewStyle().Foreground(colYellow)
	stKey     = lipgloss.NewStyle().Foreground(colCyan)
	stString  = lipgloss.NewStyle().Foreground(colGreen)
	stNumber  = lipgloss.NewStyle().Foreground(colOrange)
	stBool    = lipgloss.NewStyle().Foreground(colPurple)
	stSel     = lipgloss.NewStyle().Background(colSelBg).Foreground(colFg).Bold(true)
	stSelLo   = lipgloss.NewStyle().Background(colSelBgLo).Foreground(colFg)
	stBar     = lipgloss.NewStyle().Background(colBarBg).Foreground(colFg)
	stBarKey  = lipgloss.NewStyle().Background(colBarBg).Foreground(colAccent).Bold(true)
	stBarText = lipgloss.NewStyle().Background(colBarBg).Foreground(colMuted)
	stLogo    = lipgloss.NewStyle().Background(colAccent).Foreground(lipgloss.Color("#1E222A")).Bold(true)
)

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
