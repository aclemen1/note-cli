// Package store keeps the notes of a sphere: one Markdown file per note,
// front matter for the fields, body for the text.
package store

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/aclemen1/note-cli/internal/config"
	"github.com/aclemen1/note-cli/internal/spec"
)

const (
	Active   = "active"
	Archived = "archived"
)

var States = []string{Active, Archived}

type LogEntry struct {
	At   string `yaml:"at" json:"at"`
	By   string `yaml:"by" json:"by"`
	What string `yaml:"what" json:"what"`
}

type Note struct {
	ID string `yaml:"id" json:"id"`
	// Refs are the objects the note belongs to, as <tool>:<id>; at least one.
	Refs []string `yaml:"refs" json:"refs"`
	// Source is where the note comes from, as <tool>:<id>; one source gives one note in a sphere.
	Source  string     `yaml:"source,omitempty" json:"source,omitempty"`
	By      string     `yaml:"by,omitempty" json:"by,omitempty"`
	State   string     `yaml:"state" json:"state"`
	Created string     `yaml:"created" json:"created"`
	Updated string     `yaml:"updated,omitempty" json:"updated,omitempty"`
	Log     []LogEntry `yaml:"log,omitempty" json:"log,omitempty"`
	Body    string     `yaml:"-" json:"body"`
	File    string     `yaml:"-" json:"file"`
}

// Summary is the first non-empty line of the body.
func (n *Note) Summary() string {
	for _, l := range strings.Split(n.Body, "\n") {
		if l = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(l), "#")); l != "" {
			return l
		}
	}
	return ""
}

// HasRef says whether the note cites ref; a ref ending with : or / matches every ref it starts.
func (n *Note) HasRef(ref string) bool {
	for _, r := range n.Refs {
		if MatchRef(r, ref) {
			return true
		}
	}
	return false
}

// MatchRef says whether ref is matched by filter: equal, or a prefix ending with : or /.
func MatchRef(ref, filter string) bool {
	if strings.HasSuffix(filter, ":") || strings.HasSuffix(filter, "/") {
		return strings.HasPrefix(ref, filter)
	}
	return ref == filter
}

var refRe = regexp.MustCompile(`^[a-z][a-z0-9_-]*:\S+$`)

// CheckRef refuses a ref or source that is not <tool>:<id>.
func CheckRef(what, r string) error {
	if !refRe.MatchString(r) {
		return spec.UserError("%s %q: expected <tool>:<id>, e.g. office:P-0030, oj:RDIR-17 or contact:JMR", what, r)
	}
	return nil
}

type Store struct {
	Sphere string
	Prefix string // ids are <Prefix>N-0001
	Root   string
	VCS    string
	By     string
	Now    func() time.Time
	Warn   func(string)
}

// OpenStore opens a configured sphere.
func OpenStore(cfg *config.Config, sphere, by string) (*Store, error) {
	s, ok := cfg.Spheres[sphere]
	if !ok {
		return nil, spec.UserError("unknown sphere %q; configured: %s. Example: note init --sphere perso --root ~/note/perso", sphere, strings.Join(cfg.Names(), ", "))
	}
	if _, err := os.Stat(s.Root); err != nil {
		return nil, spec.UserError("the notes of %s are missing at %s. Run: note init --sphere %s --root %s", sphere, s.Root, sphere, s.Root)
	}
	if by == "" {
		by = "user"
	}
	return &Store{Sphere: sphere, Prefix: s.Prefix, Root: s.Root, VCS: s.VCS, By: by, Now: time.Now,
		Warn: func(m string) { fmt.Fprintln(os.Stderr, "note: warning: "+m) }}, nil
}

// Init creates the store directory and, with a VCS, its repository.
func Init(root, vcs string) error {
	if err := os.MkdirAll(filepath.Join(root, ".note"), 0o755); err != nil {
		return err
	}
	ignore := filepath.Join(root, ".gitignore")
	if _, err := os.Stat(ignore); os.IsNotExist(err) {
		if err := os.WriteFile(ignore, []byte(".note/\n"), 0o644); err != nil {
			return err
		}
	}
	switch vcs {
	case "jj":
		if _, err := os.Stat(filepath.Join(root, ".jj")); os.IsNotExist(err) {
			if out, err := runIn(root, "jj", "git", "init"); err != nil {
				return fmt.Errorf("jj git init: %v: %s", err, out)
			}
		}
	case "git":
		if _, err := os.Stat(filepath.Join(root, ".git")); os.IsNotExist(err) {
			if out, err := runIn(root, "git", "init", "-q"); err != nil {
				return fmt.Errorf("git init: %v: %s", err, out)
			}
		}
	}
	return nil
}

func runIn(dir string, name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// WriteAs runs fn under the store's lock, then commits with the message it
// returns; an empty one commits nothing.
func (s *Store) WriteAs(fn func() (string, error)) error {
	if err := os.MkdirAll(filepath.Join(s.Root, ".note"), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(s.Root, ".note", "lock"), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return spec.Locked("cannot lock the notes of %s: %v", s.Sphere, err)
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	msg, err := fn()
	if err != nil {
		return err
	}
	if msg != "" {
		s.commit(msg)
	}
	return nil
}

func (s *Store) commit(msg string) {
	var out string
	var err error
	switch s.VCS {
	case "jj":
		out, err = runIn(s.Root, "jj", "commit", "-m", msg)
	case "git":
		if out, err = runIn(s.Root, "git", "add", "-A"); err == nil {
			out, err = runIn(s.Root, "git", "commit", "-q", "--allow-empty", "-m", msg)
		}
	default:
		return
	}
	if err != nil && s.Warn != nil {
		s.Warn(fmt.Sprintf("%s commit failed in %s: %v: %s", s.VCS, s.Root, err, out))
	}
}

// Log appends a line to the note's history and stamps it updated.
func (s *Store) Log(n *Note, what string) {
	now := s.Now().Format(time.RFC3339)
	n.Log = append(n.Log, LogEntry{At: now, By: s.By, What: what})
	n.Updated = now
}

var numRe = regexp.MustCompile(`^(?:([A-Z]{1,3})N-)?0*(\d+)$`)

// NormID accepts PN-0007, pn-7 or 7 in the store of prefix P; an id of another sphere is refused.
func (s *Store) NormID(id string) (string, error) {
	m := numRe.FindStringSubmatch(strings.ToUpper(strings.TrimSpace(id)))
	if m == nil || m[2] == "0" {
		return "", spec.UserError("note id %q: expected %sN-0007 or 7", id, s.Prefix)
	}
	if m[1] != "" && m[1] != s.Prefix {
		return "", spec.UserError("note %s is not in sphere %s, whose ids start with %sN-", strings.ToUpper(id), s.Sphere, s.Prefix)
	}
	n, _ := strconv.Atoi(m[2])
	return s.ID(n), nil
}

func (s *Store) ID(n int) string { return fmt.Sprintf("%sN-%04d", s.Prefix, n) }

func (s *Store) glob() string { return filepath.Join(s.Root, s.Prefix+"N-*.md") }

func (s *Store) path(id string) string { return filepath.Join(s.Root, id+".md") }

// List reads every note, newest first.
func (s *Store) List() ([]*Note, error) {
	files, err := filepath.Glob(s.glob())
	if err != nil {
		return nil, err
	}
	var out []*Note
	for _, f := range files {
		n, err := read(f)
		if err != nil {
			if s.Warn != nil {
				s.Warn(err.Error())
			}
			continue
		}
		out = append(out, n)
	}
	Sort(out)
	return out, nil
}

// Sort orders notes newest first, then by id descending.
func Sort(ns []*Note) {
	sort.SliceStable(ns, func(i, j int) bool {
		a, b := instant(ns[i].Created), instant(ns[j].Created)
		if !a.Equal(b) {
			return a.After(b)
		}
		return ns[i].ID > ns[j].ID
	})
}

func instant(s string) time.Time {
	t, _ := time.Parse(time.RFC3339, s)
	return t
}

// Get reads one note.
func (s *Store) Get(id string) (*Note, error) {
	id, err := s.NormID(id)
	if err != nil {
		return nil, err
	}
	n, err := read(s.path(id))
	if os.IsNotExist(err) {
		return nil, spec.NotFound("no note %s in %s. List them with: note ls --sphere %s --state all", id, s.Sphere, s.Sphere)
	}
	return n, err
}

// BySource finds the note of a source, whatever its state.
func (s *Store) BySource(source string) (*Note, error) {
	ns, err := s.List()
	if err != nil {
		return nil, err
	}
	for _, n := range ns {
		if n.Source == source {
			return n, nil
		}
	}
	return nil, nil
}

// NextID claims one more than the highest id ever given in the store, so that
// the id of a removed note is never given again; call it under the lock.
func (s *Store) NextID() string {
	last := filepath.Join(s.Root, ".note", "last")
	max := 0
	if b, err := os.ReadFile(last); err == nil {
		max, _ = strconv.Atoi(strings.TrimSpace(string(b)))
	}
	files, _ := filepath.Glob(s.glob())
	for _, f := range files {
		if m := numRe.FindStringSubmatch(strings.TrimSuffix(filepath.Base(f), ".md")); m != nil {
			if n, _ := strconv.Atoi(m[2]); n > max {
				max = n
			}
		}
	}
	_ = os.WriteFile(last, []byte(strconv.Itoa(max+1)+"\n"), 0o644)
	return s.ID(max + 1)
}

// Save writes the note's file atomically.
func (s *Store) Save(n *Note) error {
	fm, err := yaml.Marshal(n)
	if err != nil {
		return err
	}
	var b bytes.Buffer
	b.WriteString("---\n")
	b.Write(fm)
	b.WriteString("---\n")
	if body := strings.TrimSpace(n.Body); body != "" {
		b.WriteString("\n" + body + "\n")
	}
	n.File = s.path(n.ID)
	tmp := n.File + ".tmp"
	if err := os.WriteFile(tmp, b.Bytes(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, n.File)
}

// Remove deletes the note's file.
func (s *Store) Remove(id string) error { return os.Remove(s.path(id)) }

func read(path string) (*Note, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	str := string(b)
	if !strings.HasPrefix(str, "---\n") {
		return nil, fmt.Errorf("%s: no front matter", path)
	}
	fm, body, ok := strings.Cut(str[4:], "\n---")
	if !ok {
		return nil, fmt.Errorf("%s: front matter not closed", path)
	}
	n := &Note{}
	if err := yaml.Unmarshal([]byte(fm), n); err != nil {
		return nil, fmt.Errorf("%s: %v", path, err)
	}
	n.Body = strings.TrimSpace(strings.TrimPrefix(body, "\n"))
	n.File = path
	if n.State == "" {
		n.State = Active
	}
	if n.Refs == nil {
		n.Refs = []string{}
	}
	return n, nil
}
