package actions

import (
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/aclemen1/tuikit/when"
	"golang.org/x/text/runes"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"

	"github.com/aclemen1/note-cli/internal/config"
	"github.com/aclemen1/note-cli/internal/spec"
	"github.com/aclemen1/note-cli/internal/store"
)

type List struct {
	Items []Item `json:"items"`
	Count int    `json:"count"`
}

// RefCount is one object cited by notes.
type RefCount struct {
	Ref   string `json:"ref"`
	Count int    `json:"count"`
	Last  string `json:"last"`
}

func stateParam() spec.Param {
	return spec.Param{Name: "state", Kind: spec.String, Default: store.Active, Enum: []string{store.Active, store.Archived, "all"}, Help: "active, archived or all."}
}

func registerList() {
	spec.Register(&spec.Action{
		Category: "note", Name: "ls", Top: true,
		Summary: "List notes of every sphere, newest first: the notes of an object with --ref.",
		Discussion: "Several --ref keep the notes that cite all of them. A ref ending with : or / keeps every ref it starts: --ref office: keeps the notes of every dossier. " +
			"--source keeps the sources that start with the value. --search ignores case and accents.",
		Params: []spec.Param{
			{Name: "ref", Kind: spec.StringList, Help: "Keep the notes that cite this ref, e.g. oj:RDIR-17 or office:; repeatable."},
			readSphereParam(),
			stateParam(),
			{Name: "source", Kind: spec.String, Help: "Keep the notes whose source starts with this, e.g. gmail:."},
			{Name: "search", Kind: spec.String, Help: "Keep the notes whose text contains this."},
			{Name: "by", Kind: spec.String, Help: "Keep the notes of this author, e.g. contact:AC."},
			{Name: "since", Kind: spec.String, Help: "Keep the notes written since: 2026-10-01, 01.10.2026, 7d (seven days ago)."},
			{Name: "order", Kind: spec.String, Default: "newest", Enum: []string{"newest", "oldest"}, Help: "newest first, or oldest first like a journal."},
			{Name: "limit", Kind: spec.String, Help: "Keep the first n notes in that order."},
		},
		Examples: []string{
			"note ls --ref oj:RDIR-17 --format text",
			"note ls --ref office: --sphere perso",
			"note ls --ref contact:JMR --order oldest",
			"note ls --search drive --since 30d",
		},
		Run:  runList,
		Text: textList,
	})
	spec.Register(&spec.Action{
		Category: "note", Name: "refs", Top: true,
		Summary:  "List the objects cited by notes, with their number of notes and the date of the last one.",
		Params:   []spec.Param{{Name: "prefix", Kind: spec.String, Help: "Keep the refs that start with this, e.g. office: or contact:J."}, readSphereParam(), stateParam()},
		Examples: []string{"note refs", "note refs --prefix contact: --format text"},
		Run:      runRefs,
		Text:     textRefs,
	})
}

// Fold lowercases s and drops its accents, for a search.
func Fold(s string) string {
	t := transform.Chain(norm.NFD, runes.Remove(runes.In(unicode.Mn)), norm.NFC)
	out, _, err := transform.String(t, s)
	if err != nil {
		out = s
	}
	return strings.ToLower(out)
}

func parseSince(s string, now time.Time) (time.Time, error) {
	if d, err := when.ParseDuration(s); err == nil {
		return now.Add(-d), nil
	}
	m, err := when.Parse(s, now)
	if err != nil {
		return time.Time{}, spec.UserError("--since: %v", err)
	}
	return m.Time(now.Location(), "00:00"), nil
}

func keepState(filter, state string) bool { return filter == "all" || filter == state }

func runList(ctx *spec.Context) (any, error) {
	cfg, err := config.Load(ctx.Config)
	if err != nil {
		return nil, err
	}
	spheres, err := ReadSpheres(ctx, cfg)
	if err != nil {
		return nil, err
	}
	var since time.Time
	if v := ctx.Str("since"); v != "" {
		if since, err = parseSince(v, Now()); err != nil {
			return nil, err
		}
	}
	limit := 0
	if v := ctx.Str("limit"); v != "" {
		if limit, err = strconv.Atoi(v); err != nil || limit < 0 {
			return nil, spec.UserError("--limit takes a number, got %q", v)
		}
	}
	refs, state, src, by := listOf(ctx, "ref"), ctx.Str("state"), ctx.Str("source"), strings.TrimSpace(ctx.Str("by"))
	search := Fold(ctx.Str("search"))
	var notes []*store.Note
	sphereOf := map[*store.Note]*store.Store{}
	for _, sp := range spheres {
		s, err := OpenStore(ctx, cfg, sp)
		if err != nil {
			return nil, err
		}
		ns, err := s.List()
		if err != nil {
			return nil, err
		}
	next:
		for _, n := range ns {
			for _, r := range refs {
				if !n.HasRef(r) {
					continue next
				}
			}
			switch {
			case !keepState(state, n.State),
				src != "" && !strings.HasPrefix(n.Source, src),
				by != "" && !strings.EqualFold(n.By, by),
				!since.IsZero() && instant(n.Created).Before(since),
				search != "" && !strings.Contains(Fold(n.Body), search):
				continue
			}
			notes = append(notes, n)
			sphereOf[n] = s
		}
	}
	store.Sort(notes)
	if ctx.Str("order") == "oldest" {
		for i, j := 0, len(notes)-1; i < j; i, j = i+1, j-1 {
			notes[i], notes[j] = notes[j], notes[i]
		}
	}
	if limit > 0 && len(notes) > limit {
		notes = notes[:limit]
	}
	out := List{Items: []Item{}}
	for _, n := range notes {
		out.Items = append(out.Items, ItemOf(sphereOf[n], n))
	}
	out.Count = len(out.Items)
	return out, nil
}

func instant(s string) time.Time {
	t, _ := time.Parse(time.RFC3339, s)
	return t
}

func runRefs(ctx *spec.Context) (any, error) {
	cfg, err := config.Load(ctx.Config)
	if err != nil {
		return nil, err
	}
	spheres, err := ReadSpheres(ctx, cfg)
	if err != nil {
		return nil, err
	}
	prefix, state := ctx.Str("prefix"), ctx.Str("state")
	byRef := map[string]*RefCount{}
	for _, sp := range spheres {
		s, err := OpenStore(ctx, cfg, sp)
		if err != nil {
			return nil, err
		}
		ns, err := s.List()
		if err != nil {
			return nil, err
		}
		for _, n := range ns {
			if !keepState(state, n.State) {
				continue
			}
			for _, r := range n.Refs {
				if !strings.HasPrefix(r, prefix) {
					continue
				}
				rc := byRef[r]
				if rc == nil {
					rc = &RefCount{Ref: r}
					byRef[r] = rc
				}
				rc.Count++
				if instant(n.Created).After(instant(rc.Last)) {
					rc.Last = n.Created
				}
			}
		}
	}
	out := []RefCount{}
	for _, rc := range byRef {
		out = append(out, *rc)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Last != out[j].Last {
			return instant(out[i].Last).After(instant(out[j].Last))
		}
		return out[i].Ref < out[j].Ref
	})
	return out, nil
}

func textList(w io.Writer, v any) {
	l, ok := v.(List)
	if !ok {
		return
	}
	if len(l.Items) == 0 {
		fmt.Fprintln(w, "aucune note")
		return
	}
	for _, it := range l.Items {
		mark := ""
		if it.State == store.Archived {
			mark = " (archivée)"
		}
		fmt.Fprintf(w, "%s  %s  %s  %s%s\n", it.ID, Stamp(it.Created), strings.Join(it.Refs, ","), it.Summary, mark)
	}
}

func textRefs(w io.Writer, v any) {
	l, ok := v.([]RefCount)
	if !ok {
		return
	}
	for _, rc := range l {
		fmt.Fprintf(w, "%-28s %3d  %s\n", rc.Ref, rc.Count, Stamp(rc.Last))
	}
}
