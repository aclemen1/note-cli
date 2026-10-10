package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/aclemen1/note-cli/internal/config"
	"github.com/aclemen1/note-cli/internal/spec"
	"github.com/aclemen1/note-cli/internal/store"
)

func model(t *testing.T) *Model {
	t.Helper()
	t.Setenv("NOTE_BY", "")
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	cfg, _ := config.Load(cfgPath)
	for _, s := range []struct{ name, prefix string }{{"perso", "P"}, {"pro", "U"}} {
		root := filepath.Join(dir, s.name)
		if err := store.Init(root, "none"); err != nil {
			t.Fatal(err)
		}
		if err := cfg.AddSphere(s.name, config.Sphere{Root: root, Prefix: s.prefix, VCS: "none"}); err != nil {
			t.Fatal(err)
		}
	}
	add := spec.Find("note", "add")
	for _, n := range []struct{ text, ref, sphere string }{
		{"Rendez-vous avec la régie", "office:P-0002", "perso"},
		{"Budget validé", "oj:RDIR-17", "pro"},
		{"Quota rétabli", "office:P-0030", "perso"},
	} {
		args, _ := spec.ArgsFrom(add, map[string]any{"text": n.text, "ref": []string{n.ref}, "sphere": n.sphere})
		if _, err := add.Run(&spec.Context{Args: args, Config: cfgPath}); err != nil {
			t.Fatal(err)
		}
	}
	cfg, _ = config.Load(cfgPath)
	m := New(cfgPath, cfg, cfg.Names())
	m.reload()
	return m
}

func press(m *Model, keys ...string) {
	for _, k := range keys {
		var msg tea.KeyPressMsg
		switch k {
		case "esc":
			msg = tea.KeyPressMsg{Code: tea.KeyEscape}
		case "enter":
			msg = tea.KeyPressMsg{Code: tea.KeyEnter}
		case "tab":
			msg = tea.KeyPressMsg{Code: tea.KeyTab}
		default:
			msg = tea.KeyPressMsg{Code: rune(k[0]), Text: k}
		}
		m.Update(msg)
	}
}

func TestFilterKeepsTheSelectedNote(t *testing.T) {
	m := model(t)
	if len(m.items) != 3 {
		t.Fatalf("items: %d", len(m.items))
	}
	press(m, "/", "r", "e", "g", "i", "e", "enter")
	if len(m.items) != 1 || m.current().ID != "PN-0001" {
		t.Fatalf("filter: %v", m.items)
	}
	press(m, "esc")
	if len(m.items) != 3 || m.current().ID != "PN-0001" {
		t.Fatalf("after esc the selection stays on PN-0001, got %s", m.current().ID)
	}
}

func TestSphereAndArchive(t *testing.T) {
	m := model(t)
	press(m, "s")
	if m.sphereName() != "perso" || len(m.items) != 2 {
		t.Fatalf("sphere perso: %d items", len(m.items))
	}
	press(m, "e")
	if len(m.items) != 1 {
		t.Fatalf("e archives: %d", len(m.items))
	}
	press(m, "2")
	if len(m.items) != 1 || m.items[0].State != store.Archived {
		t.Fatalf("view 2 lists the archived: %v", m.items)
	}
	press(m, " ")
	if len(m.items) != 0 {
		t.Fatalf("space restores an archived note: %d", len(m.items))
	}
	press(m, "1", " ", "3")
	if len(m.items) != 2 {
		t.Fatalf("view 3 lists every note: %d", len(m.items))
	}
	press(m, "tab")
	if !m.detailOff {
		t.Fatal("tab hides the detail")
	}
}

func TestRefFilterAndCompletion(t *testing.T) {
	m := model(t)
	m.ref = "office:"
	m.reload()
	if len(m.items) != 2 {
		t.Fatalf("ref office: %d", len(m.items))
	}
	if got := m.complete("rdir"); len(got) != 1 || got[0].Value != "oj:RDIR-17" {
		t.Fatalf("complete: %v", got)
	}
}

func TestSelect(t *testing.T) {
	m := model(t)
	m.selectID("pn-0002")
	if m.current().ID != "PN-0002" || m.status != "" {
		t.Fatalf("select: %s %q", m.current().ID, m.status)
	}
	m.selectID("UN-0001")
	press(m, "e")
	m.selectID("PN-0001")
	m.selectID("UN-0001")
	if m.current().ID != "UN-0001" || m.view != 2 {
		t.Fatalf("an archived note opens in the view of every note: %s view %d", m.current().ID, m.view)
	}
	m.selectID("PN-0099")
	if m.status == "" || !m.statusErr {
		t.Fatal("an unknown id says so")
	}
}

func TestSharedCompletion(t *testing.T) {
	refs := filepath.Join(t.TempDir(), "refs.yaml")
	os.WriteFile(refs, []byte("sources:\n  dossiers:\n    prefix: \"office:\"\n    static:\n      - {value: P-0051, label: \"note : outil de notes\"}\n      - {value: P-0002, label: Armoire de pharmacie}\n"), 0o644)
	t.Setenv("TUIKIT_REFS", refs)
	m := model(t)
	m.cfg.Complete = config.Complete{"refs": {"dossiers"}}
	m2 := New(m.cfgPath, m.cfg, m.spheres)
	m2.reload()
	got := m2.complete("pharmacie")
	if len(got) != 1 || got[0].Value != "office:P-0002" {
		t.Fatalf("a dossier is found by its title: %v", got)
	}
	var values []string
	for _, it := range m2.complete("office:") {
		values = append(values, it.Value)
	}
	if strings.Join(values, ",") != "office:P-0051,office:P-0002,office:P-0030" {
		t.Fatalf("shared sources first, then the cited refs not yet proposed: %v", values)
	}
	m.cfg.Complete = config.Complete{"refs": {"absent"}}
	if m3 := New(m.cfgPath, m.cfg, m.spheres); m3.shared != nil || !m3.statusErr {
		t.Fatal("an unknown source says so and keeps the cited refs")
	}
}
