package ui

import (
	"bufio"
	"context"
	"fmt"
	"hash/fnv"
	"image/color"
	"io"
	"os"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/hdweiss/coral/internal/config"
	"github.com/hdweiss/coral/internal/k8s"
	"github.com/hdweiss/coral/internal/logs"
	"github.com/hdweiss/coral/internal/yamltree"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// logFieldsKind is the key of the log field preferences in config.Fields:
// the favorites there are pinned to the lines of every log. Pins made since
// are per app, under logFieldsKind + " " + app (fieldsKind); a log shows
// both.
const logFieldsKind = "(logs)"

// fieldsKind is the key of this log's app's field preferences. The app is
// the subject's app label, else its name.
func (v *logView) fieldsKind() string {
	app := v.subject.GetName()
	for _, l := range []string{"app.kubernetes.io/name", "app", "k8s-app"} {
		if a := v.subject.GetLabels()[l]; a != "" {
			app = a
			break
		}
	}
	return logFieldsKind + " " + app
}

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
// entry as a tree. A pod with several containers shows them all, merged by
// time and each line labelled with its container, until c picks one. A
// workload (L on a deployment, say) shows all of its pods, labelled by pod.
type logView struct {
	rect    rect
	focused bool

	ctx        string                       // kube context
	subject    *unstructured.Unstructured   // the pod, or the workload
	pods       []*unstructured.Unstructured // the subject, or the workload's pods
	containers []string                     // of all pods, in Containers order
	container  string                       // "" = all containers that have a log
	previous   bool
	labelW     int // width of the source labels; 0 when there is one source

	// What to fetch: since > 0 asks for the last since of log, else the
	// last tail lines (0 = all), like k9s's 0–6 keys.
	tail  int64
	since time.Duration

	wrap   bool   // entries wrap, multi-line messages in full (w)
	noTime bool   // hide the time column (t)
	search string // highlighted, n/N jump between matching lines (ctrl+s)

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
	return newWorkloadLogView(ctx, pod, []*unstructured.Unstructured{pod}, fields)
}

// newWorkloadLogView shows the logs of pods, the pods of subject.
func newWorkloadLogView(ctx string, subject *unstructured.Unstructured, pods []*unstructured.Unstructured, fields *config.Fields) *logView {
	v := &logView{ctx: ctx, subject: subject, pods: pods, fields: fields, follow: true, tail: logTail}
	for _, p := range pods {
		for _, c := range k8s.Containers(p) {
			if !slices.Contains(v.containers, c) {
				v.containers = append(v.containers, c)
			}
		}
	}
	// One container, or the one the default-container annotation names;
	// otherwise all of them, so that a sidecar listed first (Linkerd's and
	// Istio's proxies are) doesn't hide the app's log.
	if len(v.containers) == 1 || len(pods) == 1 && pods[0].GetAnnotations()["kubectl.kubernetes.io/default-container"] != "" {
		v.container = v.containers[0]
	}
	return v
}

// logSource is one container log that a view streams.
type logSource struct {
	req   k8s.LogRequest
	label string // put before its lines when there are several sources
}

// sources lists the logs the view streams: the selected container, or all
// containers that have a log.
func (v *logView) sources() []logSource {
	var out []logSource
	for _, pod := range v.pods {
		names := k8s.LogContainers(pod)
		if v.container != "" {
			if !slices.Contains(k8s.Containers(pod), v.container) {
				continue
			}
			names = []string{v.container}
		}
		for _, c := range names {
			req := k8s.LogRequest{
				Namespace: pod.GetNamespace(), Pod: pod.GetName(), Container: c,
				Previous: v.previous, Follow: !v.previous, TailLines: v.tail,
			}
			if v.since > 0 {
				req.TailLines, req.SinceSeconds = 0, int64(v.since.Seconds())
			}
			label := c
			if len(v.pods) > 1 {
				label = pod.GetName()
				if len(names) > 1 {
					label += "/" + c
				}
			}
			out = append(out, logSource{req: req, label: label})
		}
	}
	if len(out) == 1 {
		out[0].label = ""
	}
	return out
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
	gen, kctx := v.gen, v.ctx
	srcs := v.sources()
	v.labelW = 0
	for _, s := range srcs {
		v.labelW = max(v.labelW, ansi.StringWidth(s.label))
	}

	// A goroutine per source reads and parses lines into ch, off the UI
	// goroutine since parsing JSON is the expensive part; read hands them
	// over in batches. The first read only waits for the streams to open, so
	// that an empty log shows as such instead of loading forever. With one
	// source its error ends the view; with several, it becomes a line, since
	// one container failing (not started yet, say) shouldn't hide the others.
	ch := make(chan logs.Entry, 4096)
	opened := make(chan struct{})
	var streamErr error
	var wg, opening sync.WaitGroup
	for _, src := range srcs {
		wg.Add(1)
		opening.Add(1)
		go func() {
			defer wg.Done()
			fail := func(err error) {
				if len(srcs) == 1 {
					streamErr = err
					return
				}
				e := logs.Entry{Raw: err.Error(), Level: "ERROR", Message: "log stream: " + err.Error(), Source: src.label}
				e.KubeTime, e.Time = time.Now(), time.Now()
				e.Fields = map[string]any{"message": e.Message}
				select {
				case ch <- e:
				case <-ctx.Done():
				}
			}
			r, err := p.Logs(ctx, kctx, src.req)
			opening.Done()
			if err != nil {
				fail(err)
				return
			}
			defer r.Close()
			br := bufio.NewReaderSize(r, 64*1024)
			for {
				line, err := logs.ReadLine(br, logMaxLineSz)
				if err != nil {
					if err != io.EOF && ctx.Err() == nil {
						fail(err)
					}
					return
				}
				e := logs.Parse(line)
				e.Source = src.label
				select {
				case ch <- e:
				case <-ctx.Done():
					return
				}
			}
		}()
	}
	go func() {
		opening.Wait()
		close(opened)
	}()
	go func() {
		wg.Wait()
		close(ch)
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
	if v.labelW > 0 && v.merge(msg.entries) {
		v.trim()
	} else {
		for _, e := range msg.entries {
			v.entries = append(v.entries, e)
			v.bytes += len(e.Raw)
			if v.matches(&v.entries[len(v.entries)-1]) {
				v.rows = append(v.rows, len(v.entries)-1)
			}
		}
		v.trim()
	}
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

// merge adds a batch from several streams in time order, reporting false when
// it is already in order after the existing entries (the common case once
// the streams are running), which the caller appends as usual. Each stream
// is in order on its own, but their first tails and later batches
// interleave.
func (v *logView) merge(batch []logs.Entry) bool {
	if len(batch) == 0 {
		return false
	}
	var last time.Time
	if n := len(v.entries); n > 0 {
		last = v.entries[n-1].KubeTime
	}
	inOrder := true
	for i := range batch {
		if batch[i].KubeTime.IsZero() {
			batch[i].KubeTime = last // keep it next to its neighbour
		}
		if batch[i].KubeTime.Before(last) {
			inOrder = false
		}
		last = batch[i].KubeTime
	}
	if inOrder {
		return false
	}
	var sel *logs.Entry
	if e := v.Selected(); e != nil && !v.follow {
		cp := *e
		sel = &cp
	}
	first := slices.MinFunc(batch, func(a, b logs.Entry) int { return a.KubeTime.Compare(b.KubeTime) }).KubeTime
	from := sort.Search(len(v.entries), func(i int) bool { return v.entries[i].KubeTime.After(first) })
	for _, e := range batch {
		v.bytes += len(e.Raw)
	}
	v.entries = append(v.entries, batch...)
	slices.SortStableFunc(v.entries[from:], func(a, b logs.Entry) int { return a.KubeTime.Compare(b.KubeTime) })
	v.rebuild()
	if sel != nil {
		for i, r := range v.rows {
			if e := &v.entries[r]; r >= from && e.KubeTime.Equal(sel.KubeTime) && e.Source == sel.Source && e.Raw == sel.Raw {
				v.cursor = i
				break
			}
		}
	}
	return true
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
// Wrapped entries take several lines, so then offset moves until the
// cursor's entry fits below it.
func (v *logView) scroll() {
	if !v.wrap {
		v.offset = clamp(v.offset, 0, max(len(v.rows)-v.height(), 0))
		v.offset = scrollTo(v.cursor, v.offset, v.height())
		return
	}
	v.offset = clamp(v.offset, 0, max(len(v.rows)-1, 0))
	if v.cursor < v.offset {
		v.offset = v.cursor
	}
	for v.offset < v.cursor && v.linesBetween(v.offset, v.cursor) > v.height() {
		v.offset++
	}
}

// entryHeight is the number of lines row i takes.
func (v *logView) entryHeight(i int) int {
	if !v.wrap {
		return 1
	}
	pins := v.pins()
	return len(v.entryLines(&v.entries[v.rows[i]], pins, pinLabels(pins), false, v.rect.w-2))
}

// linesBetween counts the lines of rows from to to, inclusive.
func (v *logView) linesBetween(from, to int) int {
	n := 0
	for i := from; i <= to && i < len(v.rows); i++ {
		n += v.entryHeight(i)
	}
	return n
}

// lastVisible is the last row that starts on screen.
func (v *logView) lastVisible() int {
	n, i := 0, v.offset
	for ; i < len(v.rows); i++ {
		if n += v.entryHeight(i); n >= v.height() {
			break
		}
	}
	return min(i, len(v.rows)-1)
}

// rowAt returns the row drawn at line y of the view, or -1.
func (v *logView) rowAt(y int) int {
	n := 0
	for i := v.offset; i < len(v.rows); i++ {
		if n += v.entryHeight(i); y < n {
			return i
		}
	}
	return -1
}

// rangeLabel names what was fetched: "tail 500", "all", "1m".
func (v *logView) rangeLabel() string {
	switch {
	case v.since > 0:
		if v.since%time.Hour == 0 {
			return fmt.Sprintf("since %dh", int(v.since.Hours()))
		}
		return fmt.Sprintf("since %dm", int(v.since.Minutes()))
	case v.tail == 0:
		return "all"
	}
	return fmt.Sprintf("tail %d", v.tail)
}

// logRanges are the 0–6 keys of the log view, as in k9s: tail, everything,
// then the last 1m … 1h.
var logRanges = map[string]struct {
	tail  int64
	since time.Duration
}{
	"0": {logTail, 0}, "1": {0, 0}, "2": {0, time.Minute}, "3": {0, 5 * time.Minute},
	"4": {0, 15 * time.Minute}, "5": {0, 30 * time.Minute}, "6": {0, time.Hour},
}

// searchMatches reports whether entry i matches the search.
func (v *logView) searchMatches(e *logs.Entry) bool {
	return v.search != "" && strings.Contains(strings.ToLower(e.Raw), strings.ToLower(v.search))
}

// jump moves the cursor to the next (dir 1) or previous (-1) line matching
// the search, wrapping around, and reports whether there was one.
func (v *logView) jump(dir int) bool {
	n := len(v.rows)
	for k := 1; k <= n; k++ {
		i := ((v.cursor+dir*k)%n + n) % n
		if v.searchMatches(&v.entries[v.rows[i]]) {
			v.cursor, v.follow = i, false
			v.scroll()
			return true
		}
	}
	return false
}

func (v *logView) Selected() *logs.Entry {
	if v.cursor >= 0 && v.cursor < len(v.rows) {
		return &v.entries[v.rows[v.cursor]]
	}
	return nil
}

func (v *logView) height() int { return v.rect.h - 2 }

// nextContainer cycles through all containers and each one, reporting
// whether there is more than one.
func (v *logView) nextContainer() bool {
	if len(v.containers) < 2 {
		return false
	}
	cycle := append([]string{""}, v.containers...)
	i := slices.Index(cycle, v.container)
	v.container = cycle[(i+1)%len(cycle)]
	return true
}

func (v *logView) Update(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		if c, ok := moveCursor(msg.String(), v.cursor, len(v.rows), v.height()); ok {
			v.cursor = c
			v.follow = c >= len(v.rows)-1
		}
	case clickMsg:
		i := v.rowAt(msg.y)
		if i >= 0 && i < len(v.rows) {
			v.cursor = i
			v.follow = i == len(v.rows)-1
			if msg.double {
				return emit(openDetailMsg{})
			}
		}
	case wheelMsg:
		maxOff := max(len(v.rows)-v.height(), 0)
		if v.wrap {
			maxOff = max(len(v.rows)-1, 0)
		}
		v.offset = clamp(v.offset+msg.delta, 0, maxOff)
		v.cursor = clamp(v.cursor, v.offset, v.lastVisible())
		v.cursor = clamp(v.cursor, 0, len(v.rows)-1)
		v.follow = v.cursor == len(v.rows)-1 && msg.delta > 0
		return nil
	}
	v.scroll()
	return nil
}

func (v *logView) title() string {
	t := "Logs " + v.subject.GetName()
	if len(v.pods) > 1 || v.subject.GetKind() != "Pod" {
		t = "Logs " + strings.ToLower(v.subject.GetKind()) + " " + v.subject.GetName() + fmt.Sprintf(" (%d pods)", len(v.pods))
	}
	if len(v.containers) > 1 {
		c := v.container
		if c == "" {
			c = "all containers"
		}
		t += " (" + c + ")"
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
		lines = append(lines, v.entryLines(&v.entries[v.rows[i]], pins, labels, i == v.cursor, iw)...)
	}
	lines = lines[:min(len(lines), v.height())]

	footer := []string{v.rangeLabel()}
	if v.filter != "" {
		footer = append(footer, "/"+v.filter)
	}
	if v.search != "" {
		footer = append(footer, "search: "+v.search+" (n/N)")
	}
	if v.wrap {
		footer = append(footer, "wrap")
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

// pins returns the patterns of the fields pinned to log lines: those of
// every log, then this app's.
func (v *logView) pins() []string {
	if v.fields == nil {
		return nil
	}
	var out []string
	for _, kind := range []string{logFieldsKind, v.fieldsKind()} {
		if k := v.fields.Kinds[kind]; k != nil {
			for _, p := range k.Favorites {
				if !slices.Contains(out, p) {
					out = append(out, p)
				}
			}
		}
	}
	return out
}

// togglePin pins a field to this app's lines, or unpins it wherever it is
// pinned.
func (v *logView) togglePin(pattern string) {
	if v.fields.IsFavorite(logFieldsKind, pattern) {
		v.fields.SetFavorite(logFieldsKind, pattern, false)
		return
	}
	kind := v.fieldsKind()
	v.fields.SetFavorite(kind, pattern, !v.fields.IsFavorite(kind, pattern))
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

// segment is a piece of a log line in one style; "\n" alone breaks the line.
type segment struct {
	s  string
	st lipgloss.Style
}

// entryLines draws an entry as: source, time, level, pinned fields,
// message. It is one line cut to w, or with wrap on, the whole message
// wrapped to w. Search matches are highlighted.
func (v *logView) entryLines(e *logs.Entry, pins, labels []string, selected bool, w int) []string {
	var segs []segment
	part := func(s string, st lipgloss.Style) {
		if selected {
			st = stSelLo
			if v.focused {
				st = stSel
			}
		}
		segs = append(segs, v.highlight(s, st)...)
	}
	none := lipgloss.NewStyle()

	part(" ", none)
	if v.labelW > 0 {
		part(fmt.Sprintf("%-*s ", v.labelW, e.Source), sourceStyle(e.Source))
	}
	if !e.Time.IsZero() && !v.noTime {
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
	if v.wrap {
		for i, l := range strings.Split(msg, "\n") {
			if i > 0 {
				segs = append(segs, segment{s: "\n"})
				part("  ", none)
			}
			part(untab(l), none)
		}
	} else {
		part(firstLine(msg), none)
		if strings.Contains(msg, "\n") {
			part(" ⏎", stMuted)
		}
	}
	fill := none
	if selected {
		fill = segs[0].st
	}
	return layoutSegments(segs, w, v.wrap, fill)
}

// highlight splits s into segments in st, with the search matches in the
// search style.
func (v *logView) highlight(s string, st lipgloss.Style) []segment {
	if v.search == "" {
		return []segment{{s, st}}
	}
	var out []segment
	lower, q := strings.ToLower(s), strings.ToLower(v.search)
	for {
		i := strings.Index(lower, q)
		if i < 0 || len(lower) != len(s) { // ToLower changed byte offsets: no highlight
			return append(out, segment{s, st})
		}
		if i > 0 {
			out = append(out, segment{s[:i], st})
		}
		out = append(out, segment{s[i : i+len(q)], st.Background(colYellow).Foreground(colBarBg)})
		s, lower = s[i+len(q):], lower[i+len(q):]
		if s == "" {
			return out
		}
	}
}

// layoutSegments renders segments into lines of width w: wrapped, or one
// line cut with an ellipsis. Lines are padded to w in fill, so that a
// selected entry is highlighted across the panel.
func layoutSegments(segs []segment, w int, wrap bool, fill lipgloss.Style) []string {
	var lines []string
	var cur strings.Builder
	used := 0
	flush := func() {
		line := cur.String()
		if used > w {
			line, used = ansi.Truncate(line, w, "…"), w
		}
		lines = append(lines, line+fill.Render(strings.Repeat(" ", max(w-used, 0))))
		cur.Reset()
		used = 0
	}
	for _, sg := range segs {
		if sg.s == "\n" {
			flush()
			continue
		}
		if !wrap {
			cur.WriteString(sg.st.Render(sg.s))
			used += ansi.StringWidth(sg.s)
			continue
		}
		for rest := sg.s; rest != ""; {
			if used >= w {
				flush()
			}
			piece := ansi.Truncate(rest, w-used, "")
			if piece == "" { // a wide rune at the end of a line
				if used == 0 {
					piece = string([]rune(rest)[0])
				} else {
					flush()
					continue
				}
			}
			cur.WriteString(sg.st.Render(piece))
			used += ansi.StringWidth(piece)
			rest = rest[len(piece):]
		}
	}
	flush()
	return lines
}

// sourceStyle colors a source label, the same label always the same way.
func sourceStyle(label string) lipgloss.Style {
	cols := []color.Color{colCyan, colPurple, colBlue, colOrange, colGreen, colYellow}
	h := fnv.New32a()
	h.Write([]byte(label))
	return lipgloss.NewStyle().Foreground(cols[h.Sum32()%uint32(len(cols))])
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return untab(s)
}

// save writes the shown lines (after the filter) to a file in the current
// directory, each with its kubelet timestamp and, when the view merges
// several logs, its source. It returns the file's name.
func (v *logView) save(now time.Time) (string, error) {
	c := v.container
	if c == "" {
		c = "all"
	}
	name := fmt.Sprintf("%s-%s-%s.log", v.subject.GetName(), c, now.Format("20060102-150405"))
	var b strings.Builder
	for _, r := range v.rows {
		e := &v.entries[r]
		if !e.KubeTime.IsZero() {
			b.WriteString(e.KubeTime.UTC().Format(time.RFC3339Nano) + " ")
		}
		if v.labelW > 0 {
			b.WriteString("[" + e.Source + "] ")
		}
		b.WriteString(e.Raw + "\n")
	}
	return name, os.WriteFile(name, []byte(b.String()), 0o644)
}

// leafPatterns lists the scalar fields of an entry, in tree order, for
// pinning from the line view.
func leafPatterns(fields map[string]any) []*yamltree.Node {
	var out []*yamltree.Node
	yamltree.Build(fields).Walk(func(n *yamltree.Node) {
		if !n.HasChildren() && n.Arrangeable() && n.Parent != nil {
			out = append(out, n)
		}
	})
	return out
}
