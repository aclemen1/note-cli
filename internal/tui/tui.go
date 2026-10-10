// Package tui is the terminal interface of note: a journal of the notes,
// newest first, with the detail of the selected one below.
package tui

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/aclemen1/tuikit"
	"github.com/aclemen1/tuikit/complete"

	"github.com/aclemen1/note-cli/internal/actions"
	"github.com/aclemen1/note-cli/internal/config"
	"github.com/aclemen1/note-cli/internal/spec"
	"github.com/aclemen1/note-cli/internal/store"
)

func init() {
	spec.Register(&spec.Action{
		Category: "setup", Name: "tui", Top: true,
		Summary: "Open the terminal interface: the notes, newest first, the detail of one, keys to add, edit, archive.",
		Params: []spec.Param{
			{Name: "sphere", Kind: spec.String, Help: "Show only this sphere. Defaults to every sphere."},
			{Name: "ref", Kind: spec.String, Help: "Show only the notes of this ref, e.g. office:P-0030."},
			{Name: "select", Kind: spec.String, Help: "Open on this note, selected and visible, e.g. PN-0002; an unknown id opens the TUI as usual, with a message."},
		},
		Effects:  []string{"Runs until q; every change goes through the same actions as the CLI."},
		Examples: []string{"note tui", "note tui --ref oj:RDIR-17", "note tui --select PN-0002"},
		Run: func(ctx *spec.Context) (any, error) {
			cfg, err := config.Load(ctx.Config)
			if err != nil {
				return nil, err
			}
			spheres, err := actions.ReadSpheres(&spec.Context{Args: map[string]any{}}, cfg)
			if err != nil {
				return nil, err
			}
			m := New(ctx.Config, cfg, spheres)
			if sp := ctx.Str("sphere"); sp != "" {
				for i, s := range spheres {
					if s == sp {
						m.sphereSel = i + 1
					}
				}
				if m.sphereSel == 0 {
					return nil, spec.UserError("unknown sphere %q; configured: %s", sp, strings.Join(spheres, ", "))
				}
			}
			m.ref = ctx.Str("ref")
			if w := m.watch(); w != nil {
				defer w.Close()
			}
			m.reload()
			if id := ctx.Str("select"); id != "" {
				m.selectID(id)
			}
			m.restore()
			prog := tea.NewProgram(m)
			usr1 := make(chan os.Signal, 1)
			signal.Notify(usr1, syscall.SIGUSR1)
			defer signal.Stop(usr1)
			go func() {
				for range usr1 {
					prog.Send(filesChanged{})
				}
			}()
			_, err = prog.Run()
			if err == nil && m.restart {
				err = m.reexec()
			}
			return spec.Streamed{}, err
		},
	})
}

type Model struct {
	cfgPath string
	cfg     *config.Config
	spheres []string

	sphereSel int // 0: every sphere, else spheres[sphereSel-1]
	ref       string
	filter    string
	view      int  // index in views
	detailOff bool // tab hides the detail panel

	all   []actions.Item // as read, before the text filter
	items []actions.Item
	refs  []actions.RefCount
	sel   int
	// shared proposes refs from the sources named by complete in the config.
	shared *complete.Completer

	filtering bool
	input     textinput.Model

	modal     *tuikit.Modal
	full      bool
	detailTop int
	helpOn    bool
	pendingG  bool
	status    string
	statusErr bool
	w, h      int

	events             chan struct{}
	exe                string
	exeStamp, newStamp string
	restart            bool
}

type tick struct{}

type opened struct {
	out string
	err error
}

func New(cfgPath string, cfg *config.Config, spheres []string) *Model {
	in := textinput.New()
	in.Prompt = "/"
	m := &Model{cfgPath: cfgPath, cfg: cfg, spheres: spheres, input: in, w: 80, h: 24}
	if uses := cfg.Complete["refs"]; len(uses) > 0 {
		c, missing := complete.FromConfig(uses)
		m.shared = c
		if len(missing) > 0 {
			m.say("complétion : sources absentes de "+complete.File()+" : "+strings.Join(missing, ", "), true)
		}
	}
	m.exe = executable()
	m.exeStamp = stamp(m.exe)
	return m
}

func (m *Model) Init() tea.Cmd {
	return tea.Batch(tea.RequestBackgroundColor, every(), everyBin(), m.waitFiles())
}

func every() tea.Cmd { return tea.Tick(time.Minute, func(time.Time) tea.Msg { return tick{} }) }

func (m *Model) sphereName() string {
	if m.sphereSel == 0 {
		return "toutes"
	}
	return m.spheres[m.sphereSel-1]
}

// call runs an action of the spec, as the CLI would.
func (m *Model) call(name string, args map[string]any) (any, error) {
	a := spec.Find("note", name)
	if args == nil {
		args = map[string]any{}
	}
	parsed, err := spec.ArgsFrom(a, args)
	if err != nil {
		return nil, err
	}
	ctx := &spec.Context{Args: parsed, Config: m.cfgPath, Format: "json"}
	res, err := a.Run(ctx)
	if err == nil && len(ctx.Warnings) > 0 {
		m.say(strings.Join(ctx.Warnings, " · "), true)
	}
	return res, err
}

func (m *Model) reload() {
	args := map[string]any{"state": views[m.view].state}
	if m.sphereSel > 0 {
		args["sphere"] = m.sphereName()
	}
	if m.ref != "" {
		args["ref"] = []string{m.ref}
	}
	var keep string
	if it := m.current(); it != nil {
		keep = it.ID
	}
	res, err := m.call("ls", args)
	if err != nil {
		m.say(err.Error(), true)
		return
	}
	m.all = res.(actions.List).Items
	if r, err := m.call("refs", map[string]any{"state": "all"}); err == nil {
		m.refs = r.([]actions.RefCount)
	}
	m.applyFilter(keep)
}

func (m *Model) applyFilter(keep string) {
	q := actions.Fold(strings.TrimSpace(m.filter))
	m.items = m.items[:0]
	for _, it := range m.all {
		if q == "" || strings.Contains(actions.Fold(it.ID+" "+strings.Join(it.Refs, " ")+" "+it.Source+" "+it.Body), q) {
			m.items = append(m.items, it)
		}
	}
	m.sel = min(m.sel, max(0, len(m.items)-1))
	for i, it := range m.items {
		if it.ID == keep {
			m.sel = i
		}
	}
}

func (m *Model) current() *actions.Item {
	if m.sel < 0 || m.sel >= len(m.items) {
		return nil
	}
	return &m.items[m.sel]
}

func (m *Model) say(s string, isErr bool) { m.status, m.statusErr = s, isErr }

// act runs an action on the selected note and reloads.
func (m *Model) act(name string, args map[string]any, done string) {
	it := m.current()
	if it == nil {
		return
	}
	if args == nil {
		args = map[string]any{}
	}
	args["id"] = it.ID
	if _, err := m.call(name, args); err != nil {
		m.say(err.Error(), true)
		return
	}
	m.reload()
	if m.status == "" || !m.statusErr {
		m.say(done, false)
	}
}

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
		if m.modal != nil {
			m.modal.SetSize(m.w, m.h)
		}
	case tea.BackgroundColorMsg:
		tuikit.SetDarkBackground(msg.IsDark())
		return m, nil
	case tick:
		if !m.modal.Open() {
			m.reload()
		}
		return m, every()
	case filesChanged:
		if !m.modal.Open() {
			m.reload()
		}
		return m, m.waitFiles()
	case binTick:
		if m.checkBin() {
			m.restart = true
			return m, tea.Quit
		}
		return m, everyBin()
	case opened:
		if msg.err != nil {
			m.say("ouvrir : "+msg.err.Error(), true)
		} else {
			m.say(msg.out, false)
		}
		return m, nil
	case tuikit.DoneMsg:
		m.done(msg)
		return m, nil
	case tuikit.CancelMsg:
		m.say("annulé", false)
		return m, nil
	}
	if m.modal.Open() {
		return m, m.modal.Update(msg)
	}
	k, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return m, nil
	}
	if k.String() == "ctrl+c" {
		return m, tea.Quit
	}
	if m.filtering {
		return m, m.keyFilter(k)
	}
	return m, m.key(k.String())
}

func (m *Model) keyFilter(k tea.KeyPressMsg) tea.Cmd {
	switch k.String() {
	case "esc":
		m.filtering = false
		m.input.Blur()
		m.filter = ""
		m.applyFilter(m.currentID())
		return nil
	case "enter":
		m.filtering = false
		m.input.Blur()
		return nil
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(k)
	m.filter = m.input.Value()
	m.applyFilter(m.currentID())
	return cmd
}

// selectID selects a note, switching to the view of every note if it is archived.
func (m *Model) selectID(id string) {
	want := strings.ToUpper(strings.TrimSpace(id))
	find := func() bool {
		for i, it := range m.items {
			if it.ID == want {
				m.sel, m.detailTop = i, 0
				return true
			}
		}
		return false
	}
	if find() {
		return
	}
	if m.view != 2 {
		old := m.view
		m.view = 2
		m.reload()
		if find() {
			return
		}
		m.view = old
		m.reload()
	}
	m.say("note "+want+" introuvable dans cette vue", true)
}

func (m *Model) currentID() string {
	if it := m.current(); it != nil {
		return it.ID
	}
	return ""
}

func (m *Model) openModal(title string, c tuikit.Content) tea.Cmd {
	m.modal = tuikit.NewModal(title, c).SetSize(m.w, m.h)
	return nil
}

// complete proposes the refs of the shared sources, then the refs already
// cited, the most recent first.
func (m *Model) complete(q string) []tuikit.Item {
	var out []tuikit.Item
	seen := map[string]bool{}
	if m.shared != nil {
		for _, it := range m.shared.Complete(q) {
			seen[it.Value] = true
			out = append(out, it)
		}
	}
	fq := actions.Fold(q)
	for _, rc := range m.refs {
		if !seen[rc.Ref] && (fq == "" || strings.Contains(actions.Fold(rc.Ref), fq)) {
			out = append(out, tuikit.Item{Value: rc.Ref, Label: fmt.Sprintf("%d note(s)", rc.Count)})
		}
	}
	return out
}

func (m *Model) newForm() tea.Cmd {
	choices := append([]string{}, m.spheres...)
	refs := tuikit.Refs("refs", "Réfs", m.complete).Required().Help("<outil>:<id>, par exemple office:P-0030, oj:RDIR-17, contact:JMR")
	if m.ref != "" && !strings.HasSuffix(m.ref, ":") && !strings.HasSuffix(m.ref, "/") {
		refs = refs.Default([]string{m.ref})
	}
	form := tuikit.NewForm("new",
		tuikit.Choice("sphere", "Sphère", choices...).Required(),
		refs,
		tuikit.TextArea("text", "Note").Required(),
		tuikit.Text("source", "Source").Help("facultative, <outil>:<id>"),
	)
	return m.openModal("Nouvelle note", form)
}

func (m *Model) editForm(it actions.Item) tea.Cmd {
	form := tuikit.NewForm("edit",
		tuikit.Refs("refs", "Réfs", m.complete).Required().Default(append([]string{}, it.Refs...)),
		tuikit.TextArea("text", "Note").Required().Default(it.Body),
		tuikit.Text("source", "Source").Default(it.Source).Help("vide : aucune"),
	)
	return m.openModal("Modifier "+it.ID, form)
}

func (m *Model) done(msg tuikit.DoneMsg) {
	v := msg.Values
	switch msg.ID {
	case "new":
		args := map[string]any{"text": v.String("text"), "ref": v.Strings("refs"), "sphere": v.String("sphere")}
		if s := strings.TrimSpace(v.String("source")); s != "" {
			args["source"] = s
		}
		res, err := m.call("add", args)
		if err != nil {
			m.say(err.Error(), true)
			return
		}
		id := res.(actions.Item).ID
		m.reload()
		m.applyFilter(id)
		m.say("ajoutée : "+id, false)
	case "edit":
		it := m.current()
		if it == nil {
			return
		}
		args := map[string]any{}
		refs := v.Strings("refs")
		if add := minus(refs, it.Refs); len(add) > 0 {
			args["add-ref"] = add
		}
		if rm := minus(it.Refs, refs); len(rm) > 0 {
			args["rm-ref"] = rm
		}
		if t := strings.TrimSpace(v.String("text")); t != it.Body {
			args["text"] = t
		}
		if s := strings.TrimSpace(v.String("source")); s != it.Source {
			if s == "" {
				s = "none"
			}
			args["source"] = s
		}
		if len(args) == 0 {
			m.say("rien à modifier", false)
			return
		}
		m.act("edit", args, "modifiée : "+it.ID)
	case "ref":
		m.ref = strings.TrimSpace(v.String("ref"))
		m.sel = 0
		m.reload()
		if m.ref == "" {
			m.say("réf. : toutes", false)
		} else {
			m.say("réf. : "+m.ref, false)
		}
	case "rm":
		if it := m.current(); it != nil {
			id := it.ID
			m.act("rm", nil, "supprimée : "+id)
		}
	}
}

func minus(a, b []string) []string {
	var out []string
	for _, x := range a {
		found := false
		for _, y := range b {
			if x == y {
				found = true
			}
		}
		if !found {
			out = append(out, x)
		}
	}
	return out
}

// openLinked runs open.run with {"sphere","note"} on stdin.
func (m *Model) openLinked(it actions.Item) tea.Cmd {
	if len(m.cfg.Open.Run) == 0 {
		m.say("aucune commande open.run dans la configuration", true)
		return nil
	}
	argv := make([]string, len(m.cfg.Open.Run))
	for i, a := range m.cfg.Open.Run {
		argv[i] = config.Expand(a)
	}
	in, _ := json.Marshal(map[string]any{"sphere": it.Sphere, "note": it.Note})
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
		cmd.Stdin = bytes.NewReader(in)
		out, err := cmd.CombinedOutput()
		if err != nil {
			return opened{err: fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))}
		}
		return opened{out: strings.TrimSpace(string(out))}
	}
}

func (m *Model) key(k string) tea.Cmd {
	m.status = ""
	if m.pendingG {
		m.pendingG = false
		if k == "g" {
			m.sel, m.detailTop = 0, 0
			return nil
		}
	}
	it := m.current()
	switch k {
	case "q":
		if m.full {
			m.full, m.detailTop = false, 0
			return nil
		}
		return tea.Quit
	case "?":
		m.helpOn = !m.helpOn
	case "up", "k":
		m.move(-1)
	case "down", "j":
		m.move(1)
	case "g":
		m.pendingG = true
	case "home":
		m.sel = 0
	case "end", "G":
		m.sel = max(0, len(m.items)-1)
	case "ctrl+d", "pgdown":
		m.move(max(1, m.listHeight()/2))
	case "ctrl+u", "pgup":
		m.move(-max(1, m.listHeight()/2))
	case "J":
		m.detailTop++
	case "K":
		m.detailTop = max(0, m.detailTop-1)
	case "enter", "l", "right":
		if it != nil {
			m.full, m.detailTop = true, 0
		}
	case "esc", "h", "left":
		switch {
		case m.full:
			m.full, m.detailTop = false, 0
		case m.filter != "":
			m.filter = ""
			m.input.SetValue("")
			m.applyFilter(m.currentID())
		}
	case "/":
		m.filtering = true
		m.input.SetValue(m.filter)
		m.input.CursorEnd()
		return m.input.Focus()
	case "f":
		form := tuikit.NewForm("ref", tuikit.Ref("ref", "Réf.", m.complete).Default(m.ref).Help("vide : toutes ; office: garde tous les dossiers"))
		return m.openModal("Notes d'un objet", form)
	case "F":
		m.ref = ""
		m.reload()
		m.say("réf. : toutes", false)
	case "r":
		m.reload()
		m.say("relu", false)
	case "s":
		m.sphereSel = (m.sphereSel + 1) % (len(m.spheres) + 1)
		m.reload()
		m.say("sphère : "+m.sphereName(), false)
	case "1", "2", "3":
		m.setView(int(k[0] - '1'))
	case "a":
		if m.view == 0 {
			m.setView(2)
		} else {
			m.setView(0)
		}
	case "tab":
		m.detailOff = !m.detailOff
	case "c", "n":
		return m.newForm()
	case "E":
		if it != nil {
			return m.editForm(*it)
		}
	case "e", "x":
		if it != nil {
			m.act("archive", nil, "archivée : "+it.ID)
		}
	case "u":
		if it != nil {
			m.act("restore", nil, "restaurée : "+it.ID)
		}
	case " ", "space":
		if it != nil {
			if it.State == store.Archived {
				m.act("restore", nil, "restaurée : "+it.ID)
			} else {
				m.act("archive", nil, "archivée : "+it.ID)
			}
		}
	case "#":
		if it != nil {
			return m.openModal("Supprimer", tuikit.NewConfirmTyped("rm", "Supprimer définitivement "+it.ID+" ? Recopiez son id.", it.ID))
		}
	case "o":
		if it != nil {
			return m.openLinked(*it)
		}
	}
	return nil
}

func (m *Model) move(d int) {
	if len(m.items) == 0 {
		return
	}
	m.sel = min(len(m.items)-1, max(0, m.sel+d))
	m.detailTop = 0
}

var views = []struct{ name, state string }{
	{"actives", store.Active},
	{"archivées", store.Archived},
	{"toutes", "all"},
}

func (m *Model) setView(v int) {
	m.view, m.detailTop = v, 0
	m.reload()
	m.say("vue : "+views[v].name, false)
}

func (m *Model) listHeight() int {
	if m.detailOff {
		return max(3, m.h-4)
	}
	return max(3, (m.h-4)/2)
}

var (
	cAccent = lipgloss.Color("141")
	cMuted  = lipgloss.Color("245")
	cErr    = lipgloss.Color("203")
	cPerso  = lipgloss.Color("75")
	cPro    = lipgloss.Color("215")

	sTitle = lipgloss.NewStyle().Bold(true).Foreground(cAccent)
	sMuted = lipgloss.NewStyle().Foreground(cMuted)
	sSel   = lipgloss.NewStyle().Reverse(true)
	sErr   = lipgloss.NewStyle().Foreground(cErr)
	sBold  = lipgloss.NewStyle().Bold(true)
)

func sphereStyle(sp string) lipgloss.Style {
	if sp == "pro" {
		return lipgloss.NewStyle().Foreground(cPro)
	}
	return lipgloss.NewStyle().Foreground(cPerso)
}

const help = "j/k bouger · entrée détail · tab panneau · 1–3 vue · / filtre · f réf. · F toutes les réfs · s sphère · c nouvelle · E modifier · e archiver · espace archiver/restaurer · # supprimer · o ouvrir · r relire · q quitter"

func (m *Model) View() tea.View {
	v := tea.NewView(tuikit.Overlay(m.render(), m.modal, m.w, m.h))
	v.AltScreen = true
	return v
}

func (m *Model) render() string {
	var b strings.Builder
	head := sTitle.Render("note") + sMuted.Render(fmt.Sprintf("  sphère : %s · ", m.sphereName()))
	if m.ref != "" {
		head += sBold.Render("réf. : "+m.ref) + sMuted.Render(" · ")
	}
	for i, v := range views {
		label := fmt.Sprintf("%d %s", i+1, v.name)
		if i == m.view {
			head += sBold.Render(label) + sMuted.Render(" · ")
		} else {
			head += sMuted.Render(label + " · ")
		}
	}
	head += sMuted.Render(fmt.Sprintf("%d note(s)", len(m.items)))
	b.WriteString(ansi.Truncate(head, m.w, "…") + "\n")
	if m.filtering || m.filter != "" {
		b.WriteString(m.input.View() + "\n")
	} else {
		b.WriteString("\n")
	}
	body := m.h - 3
	if m.helpOn {
		body -= 2
	}
	if m.full {
		for _, l := range m.window(m.detail(), body) {
			b.WriteString(l + "\n")
		}
	} else {
		lh := m.listHeight()
		b.WriteString(strings.Join(m.list(lh), "\n") + "\n")
		if !m.detailOff {
			b.WriteString(sMuted.Render(strings.Repeat("─", m.w)) + "\n")
			for _, l := range m.window(m.detail(), body-lh-1) {
				b.WriteString(l + "\n")
			}
		}
	}
	foot := sMuted.Render("? aide")
	if m.helpOn {
		foot = sMuted.Render(ansi.Wrap(help, m.w, ""))
	}
	if m.status != "" {
		if m.statusErr {
			foot = sErr.Render(ansi.Truncate(m.status, m.w, "…"))
		} else {
			foot = ansi.Truncate(m.status, m.w, "…")
		}
	}
	room := max(1, m.h-strings.Count(foot, "\n")-1)
	lines := strings.Split(strings.TrimRight(b.String(), "\n"), "\n")
	for len(lines) < room {
		lines = append(lines, "")
	}
	return strings.Join(lines[:room], "\n") + "\n" + foot
}

// list renders the rows around the selection.
func (m *Model) list(height int) []string {
	if len(m.items) == 0 {
		return []string{sMuted.Render("aucune note — c pour en ajouter une")}
	}
	top := max(0, min(m.sel-height/2, len(m.items)-height))
	var out []string
	for i := top; i < min(len(m.items), top+height); i++ {
		it := m.items[i]
		when := actions.Stamp(it.Created)
		if len(when) >= 16 {
			when = when[:5] + when[10:]
		}
		sp, _, _ := strings.Cut(it.ID, "N-")
		refs := strings.Join(it.Refs, ",")
		row := fmt.Sprintf("%s %s %-18s %s", when, sp, ansi.Truncate(refs, 18, "…"), it.Summary)
		if it.State == store.Archived {
			row += " (archivée)"
		}
		row = ansi.Truncate(row, m.w, "…")
		switch {
		case i == m.sel:
			row = sSel.Render(row + strings.Repeat(" ", max(0, m.w-ansi.StringWidth(row))))
		case it.State == store.Archived:
			row = sMuted.Render(row)
		default:
			row = sMuted.Render(when+" ") + sphereStyle(it.Sphere).Render(sp) + row[len(when)+len(sp)+1:]
		}
		out = append(out, row)
	}
	return out
}

func (m *Model) detail() []string {
	it := m.current()
	if it == nil {
		return nil
	}
	w := max(20, m.w)
	out := []string{sBold.Render(it.ID) + sMuted.Render(" · ") + sphereStyle(it.Sphere).Render(it.Sphere) + sMuted.Render(" · "+it.State)}
	out = append(out, sMuted.Render("réfs   ")+strings.Join(it.Refs, ", "))
	if it.Source != "" {
		out = append(out, sMuted.Render("source ")+it.Source)
	}
	out = append(out, sMuted.Render("par    ")+it.By+sMuted.Render(" · "+actions.Stamp(it.Created)))
	if m.full {
		for _, l := range it.Log {
			out = append(out, sMuted.Render("       "+actions.Stamp(l.At)+" · "+l.By+" · "+l.What))
		}
	}
	out = append(out, "")
	for _, l := range strings.Split(ansi.Wrap(it.Body, w, ""), "\n") {
		out = append(out, l)
	}
	return out
}

func (m *Model) window(lines []string, height int) []string {
	if height <= 0 {
		return nil
	}
	m.detailTop = min(m.detailTop, max(0, len(lines)-height))
	end := min(len(lines), m.detailTop+height)
	out := lines[m.detailTop:end]
	for i := range out {
		out[i] = ansi.Truncate(out[i], m.w, "…")
	}
	return out
}
