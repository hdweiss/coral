package ui

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/hdweiss/coral/internal/config"
	"github.com/hdweiss/coral/internal/k8s"
	"github.com/hdweiss/coral/internal/logs"
	"github.com/hdweiss/coral/internal/yamltree"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// logFieldsKind is the key of the log field preferences in config.Fields.
// Favorites there are the fields pinned to every log line; they are global,
// not per app.
const logFieldsKind = "(logs)"

const (
	logTail      = 500      // lines fetched when a log opens
	logMaxLines  = 20000    // older lines are dropped beyond this
	logMaxBytes  = 32 << 20 // or beyond this much raw text
	logMaxLineSz = 1 << 20  // longer lines are cut
)

// Messages of the log stream. gen ties them to one stream so that lines of a
// stream that was replaced are dropped.
type logLinesMsg struct {
	gen     int
	entries []logs.Entry
	done    bool
	err     error
}

// logView shows a container log in place of the table. Each line shows the
// time, level, pinned fields and message; the detail panel shows the selected
// entry as a tree.
type logView struct {
	rect    rect
	focused bool

	ctx        string // kube context
	pod        *unstructured.Unstructured
	containers []string
	container  string
	previous   bool

	entries []logs.Entry
	bytes   int   // raw text in entries
	rows    []int // indices into entries that match the filter
	filter  string
	cursor  int
	offset  int
	follow  bool // keep the cursor on the newest line

	gen     int
	read    tea.Cmd // reads the next batch of the current stream
	cancel  context.CancelFunc
	loading bool
	ended   bool
	err     error

	fields *config.Fields
}

func newLogView(ctx string, pod *unstructured.Unstructured, fields *config.Fields) *logView {
	v := &logView{ctx: ctx, pod: pod, containers: k8s.Containers(pod), fields: fields, follow: true}
	if len(v.containers) > 0 {
		v.container = v.containers[0]
	}
	return v
}

// start (re)opens the stream for the current container, dropping what was
// shown.
func (v *logView) start(p k8s.Provider) tea.Cmd {
	v.stop()
	v.gen++
	v.entries, v.bytes, v.rows, v.cursor, v.offset = nil, 0, nil, 0, 0
	v.follow, v.loading, v.ended, v.err = true, true, false, nil
	ctx, cancel := context.WithCancel(context.Background())
	v.cancel = cancel
	req := k8s.LogRequest{
		Namespace: v.pod.GetNamespace(), Pod: v.pod.GetName(), Container: v.container,
		Previous: v.previous, Follow: !v.previous, TailLines: logTail,
	}
	gen, kctx := v.gen, v.ctx

	// A goroutine reads and parses lines into ch, off the UI goroutine since
	// parsing JSON is the expensive part; read hands them over in batches. The
	// first read only waits for the stream to open, so that an empty log
	// shows as such instead of loading forever.
	ch := make(chan logs.Entry, 4096)
	opened := make(chan struct{})
	var streamErr error
	go func() {
		defer close(ch)
		r, err := p.Logs(ctx, kctx, req)
		if err != nil {
			streamErr = err
			close(opened)
			return
		}
		close(opened)
		defer r.Close()
		br := bufio.NewReaderSize(r, 64*1024)
		for {
			line, err := logs.ReadLine(br, logMaxLineSz)
			if err != nil {
				if err != io.EOF && ctx.Err() == nil {
					streamErr = err
				}
				return
			}
			select {
			case ch <- logs.Parse(line):
			case <-ctx.Done():
				return
			}
		}
	}()
	waitOpen := true
	var read tea.Cmd
	read = func() tea.Msg {
		if waitOpen {
			waitOpen = false
			<-opened
			return logLinesMsg{gen: gen}
		}
		e, ok := <-ch
		if !ok {
			return logLinesMsg{gen: gen, done: true, err: streamErr}
		}
		entries := []logs.Entry{e}
		for len(entries) < 2000 {
			select {
			case e, ok := <-ch:
				if !ok {
					return logLinesMsg{gen: gen, entries: entries, done: true, err: streamErr}
				}
				entries = append(entries, e)
				continue
			default:
			}
			break
		}
		return logLinesMsg{gen: gen, entries: entries}
	}
	v.read = read
	return read
}

func (v *logView) stop() {
	if v.cancel != nil {
		v.cancel()
		v.cancel = nil
	}
}

// onLines adds a batch and returns the command reading the next one.
func (v *logView) onLines(msg logLinesMsg) tea.Cmd {
	if msg.gen != v.gen {
		return nil
	}
	v.loading = false
	for _, e := range msg.entries {
		v.entries = append(v.entries, e)
		v.bytes += len(e.Raw)
		if v.matches(&v.entries[len(v.entries)-1]) {
			v.rows = append(v.rows, len(v.entries)-1)
		}
	}
	v.trim()
	if v.follow {
		v.cursor = max(len(v.rows)-1, 0)
	}
	v.scroll()
	if msg.done {
		v.ended, v.err = true, msg.err
		return nil
	}
	return v.read
}

// trim drops the oldest entries once there are more than logMaxLines or
// logMaxBytes. It waits until the buffer is well over a limit, so that the
// copy happens once per a few thousand lines rather than on every batch.
func (v *logView) trim() {
	if len(v.entries) <= logMaxLines+1000 && v.bytes <= logMaxBytes+logMaxBytes/8 {
		return
	}
	drop, bytes := 0, v.bytes
	for drop < len(v.entries)-1 && (len(v.entries)-drop > logMaxLines || bytes > logMaxBytes) {
		bytes -= len(v.entries[drop].Raw)
		drop++
	}
	if drop == 0 {
		return
	}
	v.entries = append([]logs.Entry(nil), v.entries[drop:]...)
	v.bytes = bytes
	v.cursor = max(v.cursor-v.countRowsBelow(drop), 0)
	v.rebuild()
}

// countRowsBelow counts the rows pointing at entries before index i.
func (v *logView) countRowsBelow(i int) int {
	n := 0
	for _, r := range v.rows {
		if r < i {
			n++
		}
	}
	return n
}

func (v *logView) matches(e *logs.Entry) bool {
	if v.filter == "" {
		return true
	}
	hay := strings.ToLower(e.Raw)
	for _, t := range strings.Fields(strings.ToLower(v.filter)) {
		if !strings.Contains(hay, t) {
			return false
		}
	}
	return true
}

func (v *logView) rebuild() {
	v.rows = v.rows[:0]
	for i := range v.entries {
		if v.matches(&v.entries[i]) {
			v.rows = append(v.rows, i)
		}
	}
	v.cursor = clamp(v.cursor, 0, len(v.rows)-1)
}

// SetFilter keeps the selected entry selected when it still matches.
func (v *logView) SetFilter(f string) {
	sel := -1
	if v.cursor < len(v.rows) {
		sel = v.rows[v.cursor]
	}
	v.filter = f
	v.rebuild()
	for i, r := range v.rows {
		if r == sel {
			v.cursor = i
		}
	}
	if v.follow {
		v.cursor = max(len(v.rows)-1, 0)
	}
	v.scroll()
}

// scroll keeps the cursor on screen without leaving empty rows at the bottom.
func (v *logView) scroll() {
	v.offset = clamp(v.offset, 0, max(len(v.rows)-v.height(), 0))
	v.offset = scrollTo(v.cursor, v.offset, v.height())
}

func (v *logView) Selected() *logs.Entry {
	if v.cursor >= 0 && v.cursor < len(v.rows) {
		return &v.entries[v.rows[v.cursor]]
	}
	return nil
}

func (v *logView) height() int { return v.rect.h - 2 }

// nextContainer switches to the next container, reporting whether there is
// another one.
func (v *logView) nextContainer() bool {
	if len(v.containers) < 2 {
		return false
	}
	for i, c := range v.containers {
		if c == v.container {
			v.container = v.containers[(i+1)%len(v.containers)]
			return true
		}
	}
	return false
}

func (v *logView) Update(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		if c, ok := moveCursor(msg.String(), v.cursor, len(v.rows), v.height()); ok {
			v.cursor = c
			v.follow = c >= len(v.rows)-1
		}
	case clickMsg:
		i := v.offset + msg.y
		if i >= 0 && i < len(v.rows) {
			v.cursor = i
			v.follow = i == len(v.rows)-1
			if msg.double {
				return emit(openDetailMsg{})
			}
		}
	case wheelMsg:
		v.offset = clamp(v.offset+msg.delta, 0, max(len(v.rows)-v.height(), 0))
		v.cursor = clamp(v.cursor, v.offset, v.offset+v.height()-1)
		v.cursor = clamp(v.cursor, 0, len(v.rows)-1)
		v.follow = v.cursor == len(v.rows)-1 && msg.delta > 0
		return nil
	}
	v.scroll()
	return nil
}

func (v *logView) title() string {
	t := "Logs " + v.pod.GetName()
	if len(v.containers) > 1 {
		t += " (" + v.container + ")"
	}
	if v.previous {
		t += " previous"
	}
	if v.filter != "" {
		t += fmt.Sprintf(" [%d/%d]", len(v.rows), len(v.entries))
	} else {
		t += fmt.Sprintf(" [%d]", len(v.entries))
	}
	return t
}

func (v *logView) View() string {
	iw := v.rect.w - 2
	var lines []string
	switch {
	case v.err != nil && len(v.entries) == 0:
		lines = append(lines, stErr.Render(" "+v.err.Error()))
	case v.loading:
		lines = append(lines, stMuted.Render(" loading…"))
	case len(v.rows) == 0 && v.filter != "":
		lines = append(lines, stMuted.Render(" no lines match “"+v.filter+"”"))
	case len(v.rows) == 0 && !v.ended:
		lines = append(lines, stMuted.Render(" waiting for log lines…"))
	case len(v.rows) == 0:
		lines = append(lines, stMuted.Render(" no log lines"))
	}
	pins := v.pins()
	labels := pinLabels(pins)
	for i := v.offset; i < len(v.rows) && len(lines) < v.height(); i++ {
		lines = append(lines, v.renderLine(&v.entries[v.rows[i]], pins, labels, i == v.cursor, iw))
	}

	var footer []string
	if v.filter != "" {
		footer = append(footer, "/"+v.filter)
	}
	switch {
	case v.err != nil:
		footer = append(footer, "error: "+v.err.Error())
	case v.ended:
		footer = append(footer, "ended")
	case v.follow:
		footer = append(footer, "following")
	default:
		footer = append(footer, "paused")
	}
	if len(v.rows) > 0 {
		footer = append(footer, fmt.Sprintf("%d/%d", v.cursor+1, len(v.rows)))
	}
	return frame(v.title(), strings.Join(footer, "  "), lines, v.rect.w, v.rect.h, v.focused)
}

// pins returns the patterns of the fields pinned to log lines.
func (v *logView) pins() []string {
	if v.fields == nil || v.fields.Kinds[logFieldsKind] == nil {
		return nil
	}
	return v.fields.Kinds[logFieldsKind].Favorites
}

// Keys too generic to stand alone as a label.
var genericKeys = map[string]bool{"name": true, "id": true, "version": true, "type": true, "value": true}

// pinLabels names pinned fields on the lines by the shortest key suffix that
// is unique among the pins and not just a generic word: status_code,
// service.name.
func pinLabels(pins []string) []string {
	keys := make([][]string, len(pins))
	n := make([]int, len(pins)) // suffix length per pin
	for i, p := range pins {
		keys[i] = strings.Split(yamltree.PatternLabel(p), ".")
		n[i] = 1
		if genericKeys[keys[i][len(keys[i])-1]] {
			n[i] = 2
		}
	}
	suffix := func(i int) string {
		k := keys[i]
		return strings.Join(k[max(len(k)-n[i], 0):], ".")
	}
	// Lengthen every label that collides with another until none do.
	for changed := true; changed; {
		changed = false
		count := map[string]int{}
		for i := range pins {
			count[suffix(i)]++
		}
		for i := range pins {
			if count[suffix(i)] > 1 && n[i] < len(keys[i]) {
				n[i]++
				changed = true
			}
		}
	}
	labels := make([]string, len(pins))
	for i := range pins {
		labels[i] = suffix(i)
	}
	return labels
}

func levelStyle(l string) lipgloss.Style {
	switch l {
	case "ERROR", "FATAL":
		return stErr.Bold(true)
	case "WARN":
		return stWarn
	case "INFO":
		return lipgloss.NewStyle().Foreground(colGreen)
	case "DEBUG", "TRACE":
		return stMuted
	}
	return lipgloss.NewStyle()
}

// renderLine draws an entry as: time, level, pinned fields, message.
func (v *logView) renderLine(e *logs.Entry, pins, labels []string, selected bool, w int) string {
	var plain, styled strings.Builder
	part := func(s string, st lipgloss.Style) {
		plain.WriteString(s)
		styled.WriteString(st.Render(s))
	}
	none := lipgloss.NewStyle()

	part(" ", none)
	if !e.Time.IsZero() {
		part(e.Time.Local().Format("15:04:05.000")+" ", stMuted)
	}
	lvl := e.Level
	if len(lvl) > 5 {
		lvl = lvl[:5]
	}
	part(fmt.Sprintf("%-5s ", lvl), levelStyle(e.Level))
	if e.Format != logs.Plain {
		for i, p := range pins {
			val, ok := yamltree.Lookup(e.Fields, p)
			if !ok {
				continue
			}
			part(labels[i]+"=", stMuted)
			part(firstLine(logs.Compact(val))+" ", stKey)
		}
	}
	msg := e.Message
	if e.Format != logs.Plain && msg == "" {
		msg = e.Raw
	}
	part(firstLine(msg), none)
	if strings.Contains(msg, "\n") {
		part(" ⏎", stMuted)
	}

	if selected {
		st := stSelLo
		if v.focused {
			st = stSel
		}
		return st.Render(fit(plain.String(), w))
	}
	return fit(styled.String(), w)
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return untab(s)
}
