package ui

import (
	"fmt"
	"os/exec"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/hdweiss/coralctl/internal/schema"
	"github.com/hdweiss/coralctl/internal/yamltree"
)

// The info popup ("i" in the details) shows the schema help of the selected
// field and follows the cursor until it is toggled off.

type (
	// needSchemaMsg asks the app to load the schema of the shown object.
	needSchemaMsg struct{}
	// detailSchemaMsg delivers the schema of a kind for the info popup.
	detailSchemaMsg struct {
		key    string
		schema *schema.Schema
		err    error
	}
)

// infoState is the info popup's part of the detail view.
type infoState struct {
	on        bool
	schemaKey string // context|gvk the schema below belongs to
	schema    *schema.Schema
	err       error
}

// fieldInfo is what the popup says about one field.
type fieldInfo struct {
	typ      string
	required bool
	enum     []string
	desc     string
}

// infoAt describes the field at segs of an object with schema root.
func infoAt(root *schema.Schema, segs []yamltree.Seg) (fieldInfo, bool) {
	s := root
	var field, parent *schema.Field
	for _, sg := range segs {
		if s == nil {
			return fieldInfo{}, false
		}
		switch {
		case sg.Index < 0 && s.Kind == schema.Object:
			f := s.Field(sg.Key)
			if f == nil {
				return fieldInfo{}, false
			}
			field, s = f, f.Schema
		case sg.Index < 0 && s.Kind == schema.Map, sg.Index >= 0 && s.Kind == schema.Array:
			if field != nil {
				parent = field
			}
			field, s = nil, s.Elem
		default:
			return fieldInfo{}, false
		}
	}
	if s == nil {
		return fieldInfo{}, false
	}
	fi := fieldInfo{typ: s.String(), enum: s.Enum, desc: s.Description}
	switch {
	case field != nil:
		fi.required = field.Required
		fi.desc = field.Description
	case parent != nil && fi.desc == "":
		fi.desc = "An entry of " + parent.Name + ". " + parent.Description
	case parent != nil:
		fi.desc = "An entry of " + parent.Name + ". " + fi.desc
	}
	return fi, true
}

// infoLink is a link shown in the popup, on row (counted from the box top).
type infoLink struct {
	row int
	url string
}

var urlRe = regexp.MustCompile(`https?://[^\s<>"'()\[\]]+`)

// extractLinks replaces the URLs in text with [1], [2], …, so that the text
// can wrap freely and the URLs can be shown whole on lines of their own.
func extractLinks(text string) (string, []string) {
	var urls []string
	out := urlRe.ReplaceAllStringFunc(text, func(u string) string {
		trail := ""
		for strings.ContainsAny(u[len(u)-1:], ".,;:") {
			u, trail = u[:len(u)-1], u[len(u)-1:]+trail
		}
		i := slices.Index(urls, u)
		if i < 0 {
			urls = append(urls, u)
			i = len(urls) - 1
		}
		return "[" + strconv.Itoa(i+1) + "]" + trail
	})
	return out, urls
}

// openURL opens a link in the system browser. It is a variable for tests.
var openURL = func(url string) error {
	var c *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		c = exec.Command("open", url)
	case "windows":
		c = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		c = exec.Command("xdg-open", url)
	}
	if err := c.Start(); err != nil {
		return err
	}
	go c.Wait()
	return nil
}

// infoView renders the popup for the selected node, at most maxH rows high,
// and returns where its links are.
func (d *detailView) infoView(w, maxH int) (string, []infoLink) {
	n := d.current()
	if n == nil {
		return "", nil
	}
	for n.Kind == yamltree.Line { // a line of a multi-line string
		n = n.Parent
	}
	title := n.Path
	if title == "" {
		title = d.obj.GetKind()
	}
	iw := w - 2

	var lines []string
	var urls []string
	add := func(s string) { lines = append(lines, s) }
	wrap := func(s string, st lipgloss.Style) {
		for _, l := range strings.Split(lipgloss.Wrap(strings.Join(strings.Fields(s), " "), iw-2, ""), "\n") {
			add(" " + st.Render(l))
		}
	}

	switch {
	case n.Kind == yamltree.Hidden:
		title = strings.TrimSpace(n.Parent.Path + " hidden fields")
		wrap(fmt.Sprintf("%d field(s) hidden for every %s. Open the row to see them; x or the unhide button brings one back.",
			len(n.Children), d.obj.GetKind()), stMuted)
	case d.info.err != nil:
		wrap(d.info.err.Error(), stErr)
	case d.info.schema == nil:
		add(stMuted.Render(" loading schema…"))
	default:
		fi, ok := infoAt(d.info.schema, n.Segments())
		if !ok {
			wrap("This field is not in the schema of "+d.obj.GetKind()+".", stMuted)
			break
		}
		meta := stKey.Render(fi.typ)
		if fi.required {
			meta += stMuted.Render(" · ") + stWarn.Render("required")
		}
		add(" " + meta)
		if len(fi.enum) > 0 {
			wrap("one of: "+strings.Join(fi.enum, ", "), stString)
		}
		add("")
		if fi.desc == "" {
			add(stMuted.Render(" no description"))
		} else {
			var text string
			text, urls = extractLinks(fi.desc)
			wrap(text, lipgloss.NewStyle())
		}
	}

	// Links go last, one per line and never wrapped, as terminal hyperlinks.
	// The description gives way when space is short.
	var linkLines []string
	for i, u := range urls {
		label := fmt.Sprintf(" [%d] ", i+1)
		shown := ansi.Truncate(u, iw-ansi.StringWidth(label)-1, "…")
		linkLines = append(linkLines, stMuted.Render(label)+stLink.Hyperlink(u).Render(shown))
	}
	if len(linkLines) > 0 {
		linkLines = append([]string{""}, linkLines...)
	}
	if inner := maxH - 2 - len(linkLines); len(lines) > inner && inner > 0 {
		lines = append(lines[:inner-1], stMuted.Render(" …"))
	}
	var links []infoLink
	for i, u := range urls {
		links = append(links, infoLink{row: 1 + len(lines) + 1 + i, url: u})
	}
	lines = append(lines, linkLines...)
	footer := "i close"
	if len(urls) > 0 {
		footer = "click a link to open it · i close"
	}
	return frame(title, footer, lines, w, len(lines)+2, true), links
}
