package ui

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/hdweiss/coral/internal/k8s"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// execDoneMsg reports that kubectl (or the demo shell) returned, with the
// last line it wrote to stderr: the screen comes back before anyone could
// read it.
type execDoneMsg struct {
	what   string
	err    error
	stderr string
}

// lastLine keeps the last non-empty line written to it.
type lastLine struct{ buf []byte }

func (l *lastLine) Write(p []byte) (int, error) {
	l.buf = append(l.buf, p...)
	if len(l.buf) > 4096 {
		l.buf = l.buf[len(l.buf)-4096:]
	}
	return len(p), nil
}

func (l *lastLine) String() string {
	lines := strings.Split(strings.TrimSpace(string(l.buf)), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

// shellScript prefers bash and falls back to sh, like k9s.
const shellScript = "command -v bash >/dev/null 2>&1 && exec bash || exec sh"

// openShell runs a shell in a container of pod (kubectl exec -it), after
// asking which container when there are several.
func (a *App) openShell(pod *unstructured.Unstructured) tea.Cmd {
	return a.pickContainer(pod, "Shell into", func(c string) tea.Cmd {
		return a.runKubectl("shell", pod, c, "exec", "-it", "-n", pod.GetNamespace(), pod.GetName(), "-c", c, "--", "sh", "-c", shellScript)
	})
}

// attachTo attaches to a container's main process (kubectl attach -it).
func (a *App) attachTo(pod *unstructured.Unstructured) tea.Cmd {
	return a.pickContainer(pod, "Attach to", func(c string) tea.Cmd {
		return a.runKubectl("attach", pod, c, "attach", "-it", "-n", pod.GetNamespace(), pod.GetName(), "-c", c)
	})
}

// pickContainer runs then with the pod's only running container, or the
// one picked from a menu (the default container first).
func (a *App) pickContainer(pod *unstructured.Unstructured, title string, then func(container string) tea.Cmd) tea.Cmd {
	var names []string
	for _, c := range k8s.Containers(pod) {
		if k8s.ContainerRunning(pod, c) {
			names = append(names, c)
		}
	}
	switch len(names) {
	case 0:
		a.setFlash(pod.GetName()+" has no running container", true)
		return nil
	case 1:
		return then(names[0])
	}
	var items []paletteItem
	for _, c := range names {
		c := c
		items = append(items, paletteItem{cmd: c, desc: "container", run: func() tea.Cmd { return then(c) }})
	}
	a.palette = newPalette(items, "")
	a.palette.title = title + " " + pod.GetName()
	a.palette.input.Placeholder = "container"
	return a.palette.input.Focus()
}

// runKubectl hands the terminal to kubectl with the context's arguments
// first. Demo clusters have no kubectl, so they get the local shell with a
// banner saying so.
func (a *App) runKubectl(what string, pod *unstructured.Unstructured, container string, args ...string) tea.Cmd {
	base, err := a.store.Provider().Kubectl(a.cur.Context)
	var cmd *exec.Cmd
	switch {
	case errors.Is(err, k8s.ErrDemo):
		shell := os.Getenv("SHELL")
		if shell == "" {
			shell = "/bin/sh"
		}
		banner := fmt.Sprintf("coral demo: a local %s stands in for %s %s/%s (exit to return)", shell, what, pod.GetName(), container)
		cmd = exec.Command("sh", "-c", `printf '\033[1m%s\033[0m\n' "$1"; exec "$2"`, "sh", banner, shell)
	case err != nil:
		a.setFlash(err.Error(), true)
		return nil
	default:
		path, err := exec.LookPath("kubectl")
		if err != nil {
			a.setFlash(what+" needs kubectl in $PATH", true)
			return nil
		}
		cmd = exec.Command(path, append(base, args...)...)
	}
	tail := &lastLine{}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, io.MultiWriter(os.Stderr, tail)
	return tea.ExecProcess(cmd, func(err error) tea.Msg { return execDoneMsg{what: what, err: err, stderr: tail.String()} })
}

func (a *App) onExecDone(msg execDoneMsg) tea.Cmd {
	var exit *exec.ExitError
	switch {
	case msg.err == nil:
	case errors.As(msg.err, &exit) && msg.stderr != "":
		a.setFlash(msg.what+": "+msg.stderr, true)
	case errors.As(msg.err, &exit):
		// A shell exits with its last command's status: no news.
	default:
		a.setFlash(msg.what+": "+msg.err.Error(), true)
	}
	// What happened inside may have changed the pod.
	return a.fetch(a.cur)
}
