package ui

import tea "charm.land/bubbletea/v2"

// Mouse events are translated into panel-local coordinates before being
// handed to a panel: (0,0) is the first content cell inside the border.
type (
	clickMsg struct {
		x, y   int
		double bool
	}
	wheelMsg struct{ delta int }
	// focusMsg asks the app to move focus, e.g. when h/l run past the edge
	// of a panel.
	focusMsg struct{ f focusID }
)

// plainKey and ctrlKey build key presses, for buttons that act like keys.
func plainKey(r rune) tea.KeyPressMsg { return tea.KeyPressMsg{Code: r, Text: string(r)} }
func ctrlKey(r rune) tea.KeyPressMsg  { return tea.KeyPressMsg{Code: r, Mod: tea.ModCtrl} }

func emit(msg tea.Msg) tea.Cmd { return func() tea.Msg { return msg } }

// moveCursor applies a list navigation key to cursor in a list of n items
// with page rows visible.
func moveCursor(key string, cursor, n, page int) (int, bool) {
	switch key {
	case "up", "k":
		cursor--
	case "down", "j":
		cursor++
	case "pgup", "ctrl+b":
		cursor -= max(page-1, 1)
	case "pgdown", "ctrl+f":
		cursor += max(page-1, 1)
	case "home", "g":
		cursor = 0
	case "end", "G":
		cursor = n - 1
	default:
		return cursor, false
	}
	return clamp(cursor, 0, n-1), true
}
