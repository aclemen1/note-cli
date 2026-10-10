package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestBusyJobs(t *testing.T) {
	m := model(t)
	m.w, m.h = 120, 24
	m.busy.Run("ouvrir PN-0001", func() (string, error) { select {} })
	if s := ansi.Strip(m.render()); !strings.Contains(s, "ouvrir PN-0001") {
		t.Fatalf("a running job shows in the status line:\n%s", s)
	}

	m = model(t)
	m.w, m.h = 120, 24
	finish(m, m.job("supprimer PN-0099", "rm", map[string]any{"id": "PN-0099"}))
	s := ansi.Strip(m.render())
	if !strings.Contains(s, "✗ supprimer PN-0099") || !strings.Contains(s, "! travaux") {
		t.Fatalf("a failure stays in the status line, ! in the footer:\n%s", s)
	}
	press(m, "j")
	if !strings.Contains(ansi.Strip(m.render()), "✗ supprimer PN-0099") {
		t.Fatal("a key does not clear a failure")
	}

	press(m, "/", "!")
	if m.modal.Open() || m.filter != "!" {
		t.Fatalf("! is typed in the filter: %q", m.filter)
	}
	press(m, "esc", "!")
	if !m.modal.Open() || !strings.Contains(ansi.Strip(m.modal.View()), "PN-0099") {
		t.Fatal("! opens the list of jobs")
	}
	if m.busy.Unread() != 0 || strings.Contains(ansi.Strip(m.render()), "! travaux") {
		t.Fatal("opening the list marks the failures read")
	}
}

func TestActionRunsInTheBackground(t *testing.T) {
	m := model(t)
	_, cmd := m.Update(keyPress("e"))
	if m.busy.Running() != 1 || len(m.items) != 3 {
		t.Fatalf("e starts a job, the list waits for its end: %d running, %d items", m.busy.Running(), len(m.items))
	}
	finish(m, cmd)
	if m.busy.Running() != 0 || len(m.items) != 2 {
		t.Fatalf("at its end the notes are read again: %d items", len(m.items))
	}
}
