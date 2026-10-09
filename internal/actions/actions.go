// Package actions declares every note action once; the CLI, the schema and
// the MCP server are projections of these declarations.
package actions

import (
	_ "embed"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/aclemen1/note-cli/internal/config"
	"github.com/aclemen1/note-cli/internal/spec"
	"github.com/aclemen1/note-cli/internal/store"
)

const Version = "0.1.0"

//go:embed skill.md
var skillText string

// clock replaces the real clock in tests.
var clock func() time.Time

// SetClock fixes the time the actions see; nil restores the real clock.
func SetClock(f func() time.Time) { clock = f }

func Now() time.Time {
	if clock != nil {
		return clock()
	}
	return time.Now()
}

func readSphereParam() spec.Param {
	return spec.Param{Name: "sphere", Kind: spec.String, Help: "Read only this sphere, e.g. pro. Defaults to every sphere."}
}

func writeSphereParam() spec.Param {
	return spec.Param{Name: "sphere", Kind: spec.String, Required: true,
		Help:    "Sphere the note belongs to, by what it is about: e.g. perso or pro. Never a default.",
		Missing: "a new note needs --sphere: the sphere follows what the note is about; there is no default"}
}

func idSphereParam() spec.Param {
	return spec.Param{Name: "sphere", Kind: spec.String, Help: "Sphere of a bare number; a full id (PN-0007) names its own."}
}

func checkSphere(ctx *spec.Context, cfg *config.Config, sphere string) error {
	if ctx.Spheres != nil && !contains(ctx.Spheres, sphere) {
		return spec.Forbidden("sphere %q is not served here; served: %s", sphere, strings.Join(ctx.Spheres, ", "))
	}
	if _, ok := cfg.Spheres[sphere]; !ok {
		return spec.UserError("unknown sphere %q; configured: %s. Example: note init --sphere perso --root ~/note/perso", sphere, strings.Join(cfg.Names(), ", "))
	}
	return nil
}

// ReadSpheres are the spheres a read covers: the one given, else every sphere configured or served.
func ReadSpheres(ctx *spec.Context, cfg *config.Config) ([]string, error) {
	if sphere := ctx.Str("sphere"); sphere != "" {
		return []string{sphere}, checkSphere(ctx, cfg, sphere)
	}
	if ctx.Spheres != nil {
		return ctx.Spheres, nil
	}
	if len(cfg.Spheres) == 0 {
		return nil, spec.UserError("no sphere is configured. Example: note init --sphere perso --root ~/note/perso")
	}
	return cfg.Names(), nil
}

// SphereOfNote is the sphere named by a note id's prefix, else by --sphere.
func SphereOfNote(ctx *spec.Context, cfg *config.Config, id string) (string, error) {
	given := ctx.Str("sphere")
	sphere, ok := cfg.SphereOfID(id)
	switch {
	case ok && given != "" && given != sphere:
		return "", spec.UserError("%s is a note of %s, not of %s", strings.ToUpper(id), sphere, given)
	case ok:
	case given != "":
		sphere = given
	case ctx.Spheres != nil && len(ctx.Spheres) == 1:
		sphere = ctx.Spheres[0]
	case ctx.Spheres == nil && len(cfg.Spheres) == 1:
		sphere = cfg.Names()[0]
	default:
		return "", spec.UserError("%s names no sphere: give its full id (e.g. PN-0007) or --sphere", id)
	}
	return sphere, checkSphere(ctx, cfg, sphere)
}

// Author is who writes: --by, else $NOTE_BY, else agent:mcp through MCP, else the configured by, else user.
func Author(ctx *spec.Context, cfg *config.Config) string {
	if ctx != nil {
		if by := strings.TrimSpace(ctx.Str("by")); by != "" {
			return by
		}
	}
	by := os.Getenv("NOTE_BY")
	if ctx != nil && ctx.Spheres != nil && !strings.HasPrefix(by, "agent:") {
		return "agent:mcp"
	}
	if by != "" {
		return by
	}
	if cfg.By != "" {
		return cfg.By
	}
	return "user"
}

// OpenStore opens a sphere's store with the call's author and clock.
func OpenStore(ctx *spec.Context, cfg *config.Config, sphere string) (*store.Store, error) {
	s, err := store.OpenStore(cfg, sphere, Author(ctx, cfg))
	if err != nil {
		return nil, err
	}
	s.Now = Now
	if ctx != nil {
		s.Warn = ctx.Warn
	}
	return s, nil
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

func init() {
	registerNotes()
	registerList()
	registerMeta()
}

func registerMeta() {
	spec.Register(&spec.Action{
		Category: "setup", Name: "init", Top: true,
		Summary: "Declare a sphere and create its store of notes.",
		Params: []spec.Param{
			{Name: "sphere", Kind: spec.String, Required: true, Help: "Sphere name, e.g. perso."},
			{Name: "root", Kind: spec.String, Required: true, Help: "Directory of the sphere's notes, e.g. ~/note/perso."},
			{Name: "prefix", Kind: spec.String, Help: "Capital letters that start the ids (P gives PN-0001). Defaults to the sphere's initial."},
			{Name: "vcs", Kind: spec.String, Default: "jj", Enum: []string{"jj", "git", "none"}, Help: "Version control of the store: a commit after each change."},
		},
		Effects:  []string{"Adds the sphere to the configuration file.", "Creates the store directory and, with jj or git, its repository."},
		Examples: []string{"note init --sphere perso --root ~/note/perso", "note init --sphere pro --root ~/note/pro --prefix U"},
		Run: func(ctx *spec.Context) (any, error) {
			cfg, err := config.Load(ctx.Config)
			if err != nil {
				return nil, err
			}
			name, root, vcs := ctx.Str("sphere"), config.Expand(ctx.Str("root")), ctx.Str("vcs")
			if old, ok := cfg.Spheres[name]; ok && old.Root != root {
				return nil, spec.Conflict("sphere %s already has its notes at %s", name, old.Root)
			}
			if err := store.Init(root, vcs); err != nil {
				return nil, err
			}
			s := cfg.Spheres[name]
			s.Root, s.VCS = root, vcs
			if p := ctx.Str("prefix"); p != "" {
				s.Prefix = strings.ToUpper(p)
			}
			if err := cfg.AddSphere(name, s); err != nil {
				return nil, spec.UserError("%v", err)
			}
			return map[string]any{"sphere": name, "root": root, "vcs": vcs, "config": cfg.File()}, nil
		},
	})
	spec.Register(&spec.Action{
		Category: "meta", Name: "version", Top: true, Meta: true, Summary: "Print the note version.",
		Examples: []string{"note version"},
		Run:      func(*spec.Context) (any, error) { return Version, nil },
	})
	spec.Register(&spec.Action{
		Category: "meta", Name: "schema", Top: true, Meta: true,
		Summary: "Browse actions: catalog, category, or one action's full spec.",
		Params: []spec.Param{
			{Name: "category", Kind: spec.String, Positional: true, Help: "Category to list."},
			{Name: "action", Kind: spec.String, Positional: true, Help: "Action to describe."},
			{Name: "search", Kind: spec.String, Help: "Match actions across categories."},
		},
		Examples: []string{"note schema", "note schema note", "note schema note add", "note schema --search ref"},
		Run: func(ctx *spec.Context) (any, error) {
			if q := ctx.Str("search"); q != "" {
				return spec.Search(q), nil
			}
			cat, act := ctx.Str("category"), ctx.Str("action")
			switch {
			case cat == "":
				return spec.Catalog(), nil
			case act == "":
				l := spec.ActionsIn(cat)
				if len(l) == 0 {
					return nil, spec.NotFound("no category %q. Categories: %s", cat, strings.Join(spec.Categories(), ", "))
				}
				return l, nil
			}
			a := spec.Find(cat, act)
			if a == nil {
				return nil, spec.NotFound("no action %q in %q. Try `note schema %s`", act, cat, cat)
			}
			return spec.Leaf{Action: a, Usage: spec.Usage(a)}, nil
		},
		Text: spec.TextSchema,
	})
	spec.Register(&spec.Action{
		Category: "meta", Name: "skill", Top: true, Meta: true,
		Summary: "Print the embedded agent skill, or install it for an agent harness.",
		Params: []spec.Param{
			{Name: "verb", Kind: spec.String, Positional: true, Default: "show", Enum: []string{"show", "install"}, Help: "show or install"},
			{Name: "for", Kind: spec.String, Default: "claude", Help: "Harness to install for: claude."},
			{Name: "dir", Kind: spec.String, Help: "Install into this directory instead."},
		},
		Effects:  []string{"install: writes SKILL.md into ~/.claude/skills/note/ (or --dir)."},
		Examples: []string{"note skill show", "note skill install --for claude"},
		Run: func(ctx *spec.Context) (any, error) {
			if ctx.Str("verb") != "install" {
				return strings.TrimSpace(skillText), nil
			}
			if f := ctx.Str("for"); f != "claude" && f != "claude-code" {
				return nil, spec.UserError("--for takes claude, got %q. Example: note skill install --for claude", f)
			}
			dir := config.Expand(ctx.Str("dir"))
			if dir == "" {
				home, _ := os.UserHomeDir()
				dir = filepath.Join(home, ".claude", "skills", "note")
			}
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return nil, err
			}
			p := filepath.Join(dir, "SKILL.md")
			if err := os.WriteFile(p, []byte(skillText), 0o644); err != nil {
				return nil, err
			}
			return "installed " + p, nil
		},
	})
}
