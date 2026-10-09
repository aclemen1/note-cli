package actions

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/aclemen1/tuikit/when"

	"github.com/aclemen1/note-cli/internal/config"
	"github.com/aclemen1/note-cli/internal/hooks"
	"github.com/aclemen1/note-cli/internal/spec"
	"github.com/aclemen1/note-cli/internal/store"
)

// Item is a note with the sphere it lives in.
type Item struct {
	*store.Note
	Sphere  string `json:"sphere"`
	Summary string `json:"summary"`
}

func ItemOf(s *store.Store, n *store.Note) Item {
	return Item{Note: n, Sphere: s.Sphere, Summary: n.Summary()}
}

func idParam() spec.Param {
	return spec.Param{Name: "id", Kind: spec.String, Positional: true, Required: true, Help: "Note id, e.g. PN-0007 (P: perso), UN-0007 (U: pro), or 7 with --sphere."}
}

// readText reads a text given inline, or stdin for -.
func readText(ctx *spec.Context, name string) (string, error) {
	b := ctx.Str(name)
	if b == "-" && ctx.Stdin != nil {
		raw, err := io.ReadAll(ctx.Stdin)
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(raw)), nil
	}
	return strings.TrimSpace(b), nil
}

// listOf reads a repeatable, comma-separated option.
func listOf(ctx *spec.Context, name string) []string {
	var out []string
	for _, v := range ctx.List(name) {
		for _, t := range strings.Split(v, ",") {
			if t = strings.TrimSpace(t); t != "" && !contains(out, t) {
				out = append(out, t)
			}
		}
	}
	return out
}

func checkRefs(what string, refs []string) error {
	for _, r := range refs {
		if err := store.CheckRef(what, r); err != nil {
			return err
		}
	}
	return nil
}

// parseAt reads the date of an entry: RFC 3339, ISO, 12.03.2024, hier…; a date alone is midnight.
func parseAt(s string, now time.Time) (string, error) {
	m, err := when.Parse(s, now)
	if err != nil {
		return "", spec.UserError("--at: %v", err)
	}
	return m.Time(now.Location(), "00:00").Format(time.RFC3339), nil
}

// errUnchanged tells change that the note is already as asked: nothing is saved, committed or fired.
var errUnchanged = errors.New("unchanged")

// change runs f on a note under its store's lock, saves it and fires event.
func change(ctx *spec.Context, verb, event string, f func(s *store.Store, n *store.Note) error) (any, error) {
	cfg, err := config.Load(ctx.Config)
	if err != nil {
		return nil, err
	}
	sphere, err := SphereOfNote(ctx, cfg, ctx.Str("id"))
	if err != nil {
		return nil, err
	}
	s, err := OpenStore(ctx, cfg, sphere)
	if err != nil {
		return nil, err
	}
	var out *store.Note
	var prev string
	changed := false
	err = s.WriteAs(func() (string, error) {
		n, err := s.Get(ctx.Str("id"))
		if err != nil {
			return "", err
		}
		out, prev = n, n.State
		if err := f(s, n); err == errUnchanged {
			return "", nil
		} else if err != nil {
			return "", err
		}
		if err := s.Save(n); err != nil {
			return "", err
		}
		changed = true
		return fmt.Sprintf("%s %s: %s", verb, n.ID, n.Summary()), nil
	})
	if err != nil {
		return nil, err
	}
	if changed {
		fire(ctx, cfg, s, event, out, prev)
	}
	return ItemOf(s, out), nil
}

func fire(ctx *spec.Context, cfg *config.Config, s *store.Store, event string, n *store.Note, prev string) {
	if len(cfg.Hooks) == 0 {
		return
	}
	hooks.Fire(cfg, hooks.Event{Event: event, Sphere: s.Sphere, PreviousState: prev, By: s.By, Note: n}, ctx.Warn)
}

func registerNotes() {
	spec.Register(&spec.Action{
		Category: "note", Name: "add", Top: true,
		Summary: "Add a note: an information tied to one or more objects, with no date to meet and no action.",
		Discussion: "Each ref names an object the note belongs to, as <tool>:<id> (office:P-0030, oj:RDIR-17, contact:JMR); at least one. " +
			"Something to do is a task, a date not to miss is a due, a rule of conduct is a guard, knowledge to find again without the object is a memory. " +
			"One source gives one note in a sphere: a second add with the same --source is refused, or updates the note with --upsert.",
		Params: []spec.Param{
			{Name: "text", Kind: spec.String, Positional: true, Required: true, Help: "Text of the note, in Markdown; - reads stdin."},
			{Name: "ref", Kind: spec.StringList, Required: true, Help: "Object the note belongs to, as <tool>:<id>; repeatable or comma-separated.",
				Missing: "a note needs at least one --ref: the object it belongs to, as <tool>:<id>"},
			writeSphereParam(),
			{Name: "source", Kind: spec.String, Help: "Where the note comes from, as <tool>:<id>, e.g. gmail:thread/abc; one note per source."},
			{Name: "by", Kind: spec.String, Help: "Author, e.g. contact:AC or agent:P-0051. Defaults to $NOTE_BY, else by of the config."},
			{Name: "at", Kind: spec.String, Help: "Date of the entry when it is not now: 2026-10-02, 02.10.2026 14:00, hier, RFC 3339."},
			{Name: "upsert", Kind: spec.Bool, Help: "If --source already has a note, update its text and add the refs instead of refusing."},
		},
		Effects: []string{"Writes <root>/<prefix>N-0001.md and commits.", "Fires the added hooks (edited with --upsert)."},
		Examples: []string{
			`note add "Drive rapatrie 1,2 Go en double, laissé tel quel." --ref office:P-0030 --sphere perso`,
			`note add "Préfère être appelé le matin." --ref contact:JMR --ref oj:RDIR-17 --sphere pro`,
			`echo "texte" | note add - --ref office:P-0030 --sphere perso --source office:P-0030#2 --upsert`,
		},
		Run:  runAdd,
		Text: textItem,
	})
	spec.Register(&spec.Action{
		Category: "note", Name: "show", Top: true,
		Summary:  "Show a note: refs, source, author, dates, history and text.",
		Params:   []spec.Param{idParam(), idSphereParam()},
		Examples: []string{"note show PN-0007", "note show 7 --sphere perso"},
		Run: func(ctx *spec.Context) (any, error) {
			cfg, err := config.Load(ctx.Config)
			if err != nil {
				return nil, err
			}
			sphere, err := SphereOfNote(ctx, cfg, ctx.Str("id"))
			if err != nil {
				return nil, err
			}
			s, err := OpenStore(ctx, cfg, sphere)
			if err != nil {
				return nil, err
			}
			n, err := s.Get(ctx.Str("id"))
			if err != nil {
				return nil, err
			}
			return ItemOf(s, n), nil
		},
		Text: textItem,
	})
	spec.Register(&spec.Action{
		Category: "note", Name: "edit", Top: true,
		Summary:    "Change a note's text, refs or source; the history keeps who changed what.",
		Discussion: "--add-ref adds refs, --rm-ref removes some; a note keeps at least one ref.",
		Params: []spec.Param{
			idParam(),
			{Name: "text", Kind: spec.String, Help: "New text; - reads stdin."},
			{Name: "add-ref", Kind: spec.StringList, Help: "Ref to add, as <tool>:<id>; repeatable or comma-separated."},
			{Name: "rm-ref", Kind: spec.StringList, Help: "Ref to remove; repeatable or comma-separated."},
			{Name: "source", Kind: spec.String, Help: "New source, or none."},
			idSphereParam(),
		},
		Effects:  []string{"Rewrites the note and commits; fires the edited hooks. Nothing happens when nothing changes."},
		Examples: []string{`note edit PN-0007 --text "Quota rétabli."`, "note edit PN-0007 --add-ref contact:JMR --rm-ref office:P-0030"},
		Run: func(ctx *spec.Context) (any, error) {
			return change(ctx, "edit", "edited", func(s *store.Store, n *store.Note) error { return edit(ctx, s, n) })
		},
		Text: textItem,
	})
	for _, m := range []struct{ name, event, state, summary string }{
		{"archive", "archived", store.Archived, "Archive a note: it leaves the default lists and stays readable with --state all."},
		{"restore", "restored", store.Active, "Bring an archived note back to the active ones."},
	} {
		m := m
		spec.Register(&spec.Action{
			Category: "note", Name: m.name, Top: true, Summary: m.summary,
			Params:   []spec.Param{idParam(), idSphereParam()},
			Effects:  []string{"Sets the state and commits; fires the " + m.event + " hooks. Nothing happens when the note is already " + m.state + "."},
			Examples: []string{"note " + m.name + " PN-0007"},
			Run: func(ctx *spec.Context) (any, error) {
				return change(ctx, m.name, m.event, func(s *store.Store, n *store.Note) error {
					if n.State == m.state {
						return errUnchanged
					}
					n.State = m.state
					s.Log(n, m.event)
					return nil
				})
			},
			Text: textItem,
		})
	}
	spec.Register(&spec.Action{
		Category: "note", Name: "rm", Top: true, Destructive: true,
		Summary:  "Delete a note's file. Prefer archive, which keeps it readable.",
		Params:   []spec.Param{idParam(), idSphereParam()},
		Effects:  []string{"Deletes the note's file and commits; the VCS keeps the old version. Fires the removed hooks."},
		Examples: []string{"note rm PN-0007"},
		Run: func(ctx *spec.Context) (any, error) {
			cfg, err := config.Load(ctx.Config)
			if err != nil {
				return nil, err
			}
			sphere, err := SphereOfNote(ctx, cfg, ctx.Str("id"))
			if err != nil {
				return nil, err
			}
			s, err := OpenStore(ctx, cfg, sphere)
			if err != nil {
				return nil, err
			}
			var gone *store.Note
			err = s.WriteAs(func() (string, error) {
				n, err := s.Get(ctx.Str("id"))
				if err != nil {
					return "", err
				}
				gone = n
				return fmt.Sprintf("rm %s: %s", n.ID, n.Summary()), s.Remove(n.ID)
			})
			if err != nil {
				return nil, err
			}
			fire(ctx, cfg, s, "removed", gone, gone.State)
			return map[string]string{"removed": gone.ID}, nil
		},
	})
}

func runAdd(ctx *spec.Context) (any, error) {
	cfg, err := config.Load(ctx.Config)
	if err != nil {
		return nil, err
	}
	sphere := ctx.Str("sphere")
	if err := checkSphere(ctx, cfg, sphere); err != nil {
		return nil, err
	}
	text, err := readText(ctx, "text")
	if err != nil {
		return nil, err
	}
	if text == "" {
		return nil, spec.UserError("the note is empty. Example: note add \"Quota rétabli.\" --ref office:P-0030 --sphere perso")
	}
	refs := listOf(ctx, "ref")
	if len(refs) == 0 {
		return nil, spec.UserError("a note needs at least one --ref, as <tool>:<id>")
	}
	if err := checkRefs("ref", refs); err != nil {
		return nil, err
	}
	source := strings.TrimSpace(ctx.Str("source"))
	if source != "" {
		if err := store.CheckRef("source", source); err != nil {
			return nil, err
		}
	}
	s, err := OpenStore(ctx, cfg, sphere)
	if err != nil {
		return nil, err
	}
	created := Now().Format(time.RFC3339)
	if at := ctx.Str("at"); at != "" {
		if created, err = parseAt(at, Now()); err != nil {
			return nil, err
		}
	}
	var out *store.Note
	var event, prev string
	err = s.WriteAs(func() (string, error) {
		if source != "" {
			old, err := s.BySource(source)
			if err != nil {
				return "", err
			}
			if old != nil {
				if !ctx.Bool("upsert") {
					return "", spec.Conflict("source %s already has note %s; --upsert updates it", source, old.ID)
				}
				out, prev = old, old.State
				var what []string
				if old.Body != text {
					old.Body = text
					what = append(what, "text")
				}
				for _, r := range refs {
					if !contains(old.Refs, r) {
						old.Refs = append(old.Refs, r)
						what = append(what, "+ref "+r)
					}
				}
				if len(what) == 0 {
					return "", nil
				}
				s.Log(old, "upsert: "+strings.Join(what, ", "))
				if err := s.Save(old); err != nil {
					return "", err
				}
				event = "edited"
				return fmt.Sprintf("edit %s: %s", old.ID, old.Summary()), nil
			}
		}
		n := &store.Note{ID: s.NextID(), Refs: refs, Source: source, By: s.By, State: store.Active, Created: created, Body: text}
		if err := s.Save(n); err != nil {
			return "", err
		}
		out, event = n, "added"
		return fmt.Sprintf("add %s: %s", n.ID, n.Summary()), nil
	})
	if err != nil {
		return nil, err
	}
	if event != "" {
		fire(ctx, cfg, s, event, out, prev)
	}
	return ItemOf(s, out), nil
}

func edit(ctx *spec.Context, s *store.Store, n *store.Note) error {
	var what []string
	if _, ok := ctx.Args["text"]; ok {
		text, err := readText(ctx, "text")
		if err != nil {
			return err
		}
		if text == "" {
			return spec.UserError("--text is empty; archive the note instead")
		}
		if text != n.Body {
			n.Body = text
			what = append(what, "text")
		}
	}
	add, del := listOf(ctx, "add-ref"), listOf(ctx, "rm-ref")
	if err := checkRefs("ref", add); err != nil {
		return err
	}
	for _, r := range del {
		if contains(n.Refs, r) {
			n.Refs = without(n.Refs, r)
			what = append(what, "-ref "+r)
		}
	}
	for _, r := range add {
		if !contains(n.Refs, r) {
			n.Refs = append(n.Refs, r)
			what = append(what, "+ref "+r)
		}
	}
	if len(n.Refs) == 0 {
		return spec.UserError("a note keeps at least one ref; add one with --add-ref, or archive the note")
	}
	if _, ok := ctx.Args["source"]; ok {
		src := strings.TrimSpace(ctx.Str("source"))
		if src == "none" {
			src = ""
		}
		if src != "" {
			if err := store.CheckRef("source", src); err != nil {
				return err
			}
			if other, err := s.BySource(src); err != nil {
				return err
			} else if other != nil && other.ID != n.ID {
				return spec.Conflict("source %s already has note %s", src, other.ID)
			}
		}
		if src != n.Source {
			n.Source = src
			what = append(what, "source")
		}
	}
	if len(what) == 0 {
		return errUnchanged
	}
	s.Log(n, "edit: "+strings.Join(what, ", "))
	return nil
}

func without(l []string, x string) []string {
	out := []string{}
	for _, y := range l {
		if y != x {
			out = append(out, y)
		}
	}
	return out
}

// Stamp shows an RFC 3339 time as 09.10.2026 18:30 in the local zone.
func Stamp(s string) string {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return s
	}
	return t.In(Now().Location()).Format("02.01.2006 15:04")
}

func textItem(w io.Writer, v any) {
	it, ok := v.(Item)
	if !ok {
		return
	}
	fmt.Fprintf(w, "%s · %s · %s\n", it.ID, it.Sphere, it.State)
	fmt.Fprintf(w, "refs     %s\n", strings.Join(it.Refs, ", "))
	if it.Source != "" {
		fmt.Fprintf(w, "source   %s\n", it.Source)
	}
	fmt.Fprintf(w, "par      %s · %s\n", it.By, Stamp(it.Created))
	for _, l := range it.Log {
		fmt.Fprintf(w, "         %s · %s · %s\n", Stamp(l.At), l.By, l.What)
	}
	fmt.Fprintf(w, "\n%s\n", it.Body)
}
