package edit

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/yaml"
)

// Session is one object being edited in the user's editor, possibly over
// several rounds when the server rejects a change.
type Session struct {
	path     string
	desc     string         // e.g. "Deployment shop/cart"
	note     string         // extra header line, e.g. what was added
	original map[string]any // the object as on the server, in JSON form
	lastTry  map[string]any // the last content the server rejected

	written   []byte // what we last wrote, to tell whether the user saved
	writtenAt time.Time
}

// Outcome of reading back the edited file.
type Outcome int

const (
	Changed   Outcome = iota
	NotSaved          // the user quit without saving
	Unchanged         // saved, but equal to the object on the server
	Abandoned         // saved, but equal to the last rejected attempt
)

// Start writes the object to a temporary file for editing. orig is the object
// as it is on the server; body is what to show, normally orig itself or orig
// with a new field inserted. note, if any, is added to the header.
func Start(orig *unstructured.Unstructured, body map[string]any, note string) (*Session, []byte, error) {
	s := &Session{desc: describe(orig), note: note}
	var err error
	if s.original, err = jsonForm(withoutManagedFields(orig.Object)); err != nil {
		return nil, nil, err
	}
	f, err := os.CreateTemp("", fmt.Sprintf("coralctl-%s-%s-*.yaml",
		strings.ToLower(orig.GetKind()), orig.GetName()))
	if err != nil {
		return nil, nil, err
	}
	s.path = f.Name()
	f.Close()
	doc, err := ToYAML(body)
	if err != nil {
		s.Close()
		return nil, nil, err
	}
	if err := s.write(doc, nil); err != nil {
		s.Close()
		return nil, nil, err
	}
	return s, doc, nil
}

func describe(o *unstructured.Unstructured) string {
	name := o.GetName()
	if ns := o.GetNamespace(); ns != "" {
		name = ns + "/" + name
	}
	return o.GetKind() + " " + name
}

// write puts the comment header, with problem if any, above doc.
func (s *Session) write(doc []byte, problem error) error {
	content := append([]byte(s.header(problem)), doc...)
	if err := os.WriteFile(s.path, content, 0o600); err != nil {
		return err
	}
	fi, err := os.Stat(s.path)
	if err != nil {
		return err
	}
	s.written, s.writtenAt = content, fi.ModTime()
	return nil
}

func (s *Session) header(problem error) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Editing %s.\n", s.desc)
	if s.note != "" {
		b.WriteString("# " + s.note + "\n")
	}
	b.WriteString("# Save and quit to apply. Quit without saving to cancel.\n")
	b.WriteString("# Lines starting with '#' are ignored.\n")
	if problem != nil {
		b.WriteString("#\n")
		for _, l := range strings.Split(strings.TrimSpace(problem.Error()), "\n") {
			b.WriteString("# error: " + l + "\n")
		}
	}
	b.WriteString("#\n")
	return b.String()
}

// Line converts a line of the document into a line of the file.
func (s *Session) Line(docLine int, problem error) int {
	if docLine <= 0 {
		return 0
	}
	return docLine + strings.Count(s.header(problem), "\n")
}

// Read parses the edited file. It returns the new object when the content
// changed, or the reason nothing should be applied.
func (s *Session) Read() (*unstructured.Unstructured, Outcome, error) {
	raw, err := os.ReadFile(s.path)
	if err != nil {
		return nil, 0, err
	}
	if fi, err := os.Stat(s.path); err == nil && fi.ModTime().Equal(s.writtenAt) && bytes.Equal(raw, s.written) {
		return nil, NotSaved, nil
	}
	body := stripComments(raw)
	if len(bytes.TrimSpace(body)) == 0 {
		return nil, Unchanged, nil
	}
	js, err := yaml.YAMLToJSON(body)
	if err != nil {
		return nil, 0, fmt.Errorf("invalid YAML: %w", err)
	}
	var obj map[string]any
	if err := json.Unmarshal(js, &obj); err != nil {
		return nil, 0, fmt.Errorf("invalid YAML: %w", err)
	}
	if obj == nil {
		return nil, 0, errors.New("the document is not an object")
	}
	switch {
	case reflect.DeepEqual(obj, s.original):
		return nil, Unchanged, nil
	case s.lastTry != nil && reflect.DeepEqual(obj, s.lastTry):
		return nil, Abandoned, nil
	}
	u := &unstructured.Unstructured{}
	if err := u.UnmarshalJSON(js); err != nil {
		return nil, 0, err
	}
	return u, Changed, nil
}

// Reject records that the current content failed with problem and puts the
// error at the top of the file for the next round.
func (s *Session) Reject(problem error) error {
	raw, err := os.ReadFile(s.path)
	if err != nil {
		return err
	}
	body := stripComments(raw)
	if js, err := yaml.YAMLToJSON(body); err == nil {
		var obj map[string]any
		if json.Unmarshal(js, &obj) == nil {
			s.lastTry = obj
		}
	}
	return s.write(bytes.TrimLeft(body, "\n"), problem)
}

// Close removes the temporary file.
func (s *Session) Close() {
	if s.path != "" {
		os.Remove(s.path)
	}
}

// Command returns the editor command for the file, placing the cursor on
// line when the editor supports it.
func (s *Session) Command(line int) *exec.Cmd {
	return editorCommand(s.path, line)
}

// stripComments drops the leading comment block that the session writes.
func stripComments(raw []byte) []byte {
	lines := bytes.SplitAfter(raw, []byte("\n"))
	i := 0
	for i < len(lines) && bytes.HasPrefix(bytes.TrimSpace(lines[i]), []byte("#")) {
		i++
	}
	return bytes.Join(lines[i:], nil)
}

func jsonForm(v map[string]any) (map[string]any, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	return out, json.Unmarshal(b, &out)
}

// Editor returns the user's editor command line: $KUBE_EDITOR, $VISUAL or
// $EDITOR, falling back to vi.
func Editor() []string {
	for _, env := range []string{"KUBE_EDITOR", "VISUAL", "EDITOR"} {
		if f := strings.Fields(os.Getenv(env)); len(f) > 0 {
			return f
		}
	}
	return []string{"vi"}
}

func editorCommand(path string, line int) *exec.Cmd {
	argv := Editor()
	args := argv[1:]
	if line > 0 {
		switch filepath.Base(argv[0]) {
		case "vi", "vim", "nvim", "nano", "emacs", "emacsclient", "micro", "kak", "joe", "mg", "ne", "jed", "mcedit":
			args = append(args, "+"+strconv.Itoa(line), path)
		case "hx", "helix", "subl", "zed":
			args = append(args, path+":"+strconv.Itoa(line))
		case "code", "codium", "cursor":
			args = append(args, "--goto", path+":"+strconv.Itoa(line))
		default:
			args = append(args, path)
		}
	} else {
		args = append(args, path)
	}
	return exec.Command(argv[0], args...)
}
