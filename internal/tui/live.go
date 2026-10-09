package tui

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/fsnotify/fsnotify"
)

// filesChanged says that a note file changed.
type filesChanged struct{}

// binTick checks whether the binary was replaced.
type binTick struct{}

const (
	settle   = 300 * time.Millisecond
	binEvery = 2 * time.Second
	stateEnv = "NOTE_TUI_STATE"
)

// watch starts watching the stores; nil if it cannot.
func (m *Model) watch() *fsnotify.Watcher {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return nil
	}
	for _, sp := range m.spheres {
		if s, ok := m.cfg.Spheres[sp]; ok {
			_ = w.Add(s.Root)
		}
	}
	m.events = make(chan struct{}, 1)
	go func() {
		var timer *time.Timer
		for {
			select {
			case e, ok := <-w.Events:
				if !ok {
					return
				}
				if !strings.HasSuffix(e.Name, ".md") {
					continue
				}
				if timer != nil {
					timer.Stop()
				}
				timer = time.AfterFunc(settle, func() {
					select {
					case m.events <- struct{}{}:
					default:
					}
				})
			case _, ok := <-w.Errors:
				if !ok {
					return
				}
			}
		}
	}()
	return w
}

func (m *Model) waitFiles() tea.Cmd {
	if m.events == nil {
		return nil
	}
	ch := m.events
	return func() tea.Msg { <-ch; return filesChanged{} }
}

func everyBin() tea.Cmd { return tea.Tick(binEvery, func(time.Time) tea.Msg { return binTick{} }) }

// stamp identifies the binary on disk: a new install changes it.
func stamp(path string) string {
	fi, err := os.Stat(path)
	if err != nil || fi.Size() == 0 {
		return ""
	}
	return fmt.Sprintf("%s/%d", fi.ModTime().Format(time.RFC3339Nano), fi.Size())
}

// checkBin asks for a restart once a new binary has stayed the same for two checks,
// and only when nothing is being typed.
func (m *Model) checkBin() bool {
	if m.exe == "" {
		return false
	}
	now := stamp(m.exe)
	switch {
	case now == "" || now == m.exeStamp:
		m.newStamp = ""
		return false
	case now != m.newStamp:
		m.newStamp = now
		return false
	}
	if m.modal.Open() || m.filtering {
		m.say("nouvelle version installée : relance dès la fin de la saisie", false)
		return false
	}
	return true
}

// saved is what a restart keeps.
type saved struct {
	Sphere   int    `json:"sphere"`
	Ref      string `json:"ref,omitempty"`
	Filter   string `json:"filter,omitempty"`
	Archived bool   `json:"archived,omitempty"`
	Sel      string `json:"sel,omitempty"`
	Full     bool   `json:"full,omitempty"`
	Help     bool   `json:"help,omitempty"`
}

func (m *Model) restore() {
	raw := os.Getenv(stateEnv)
	if raw == "" {
		return
	}
	os.Unsetenv(stateEnv)
	var s saved
	if json.Unmarshal([]byte(raw), &s) != nil {
		return
	}
	if s.Sphere >= 0 && s.Sphere <= len(m.spheres) {
		m.sphereSel = s.Sphere
	}
	m.ref, m.filter, m.archived, m.full, m.helpOn = s.Ref, s.Filter, s.Archived, s.Full, s.Help
	m.input.SetValue(s.Filter)
	m.reload()
	m.applyFilter(s.Sel)
	m.say("nouvelle version chargée", false)
}

// reexec replaces the process with the new binary, on the same view and selection.
func (m *Model) reexec() error {
	s := saved{Sphere: m.sphereSel, Ref: m.ref, Filter: m.filter, Archived: m.archived, Full: m.full, Help: m.helpOn}
	if it := m.current(); it != nil {
		s.Sel = it.ID
	}
	b, _ := json.Marshal(s)
	env := append(os.Environ(), stateEnv+"="+string(b))
	return syscall.Exec(m.exe, os.Args, env)
}

func executable() string {
	p, err := os.Executable()
	if err != nil {
		return ""
	}
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}
