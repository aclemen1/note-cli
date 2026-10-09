// Package config reads ~/.config/note/config.yaml.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

type Config struct {
	// By is the author written on a note when neither --by nor $NOTE_BY gives one, e.g. contact:AC.
	By      string            `yaml:"by,omitempty"`
	Spheres map[string]Sphere `yaml:"spheres"`
	Hooks   []Hook            `yaml:"hooks,omitempty"`
	// Open is the command the TUI runs on o: it opens what a note refers to.
	Open Open `yaml:"open,omitempty"`

	path string
}

// Hook runs a command after an event of a note; the event comes as JSON on stdin.
type Hook struct {
	Events  []string `yaml:"events,omitempty"`  // empty: every event
	Sphere  string   `yaml:"sphere,omitempty"`  // keep notes of this sphere
	Source  string   `yaml:"source,omitempty"`  // keep notes whose source starts with this
	Ref     string   `yaml:"ref,omitempty"`     // keep notes with a ref that starts with this
	Run     []string `yaml:"run"`               // argv; a leading ~ is expanded
	Timeout string   `yaml:"timeout,omitempty"` // default 30s
}

// Open reads {"sphere","note"} on stdin and opens what the note refers to; what it prints is shown.
type Open struct {
	Run []string `yaml:"run,omitempty"`
}

// HookEvents are the events a hook can listen to.
var HookEvents = []string{"added", "edited", "archived", "restored", "removed"}

type Sphere struct {
	Root string `yaml:"root"`
	// Prefix starts the ids of the sphere's notes: P gives PN-0001. Defaults
	// to the sphere's initial; two spheres never share one.
	Prefix string `yaml:"prefix,omitempty"`
	VCS    string `yaml:"vcs,omitempty"` // jj (default), git, none
}

var (
	sphereName = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)
	prefixRe   = regexp.MustCompile(`^[A-Z]{1,3}$`)
)

// Path resolves the configuration file: the flag, then NOTE_CONFIG, then
// ~/.config/note/config.yaml.
func Path(flag string) string {
	if flag != "" {
		return Expand(flag)
	}
	if env := os.Getenv("NOTE_CONFIG"); env != "" {
		return Expand(env)
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "note", "config.yaml")
}

// Load reads the configuration. A missing file yields an empty one.
func Load(flag string) (*Config, error) {
	p := Path(flag)
	c := &Config{Spheres: map[string]Sphere{}, path: p}
	b, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		c.fill()
		return c, nil
	}
	if err != nil {
		return nil, err
	}
	if err := yaml.Unmarshal(b, c); err != nil {
		return nil, fmt.Errorf("%s: %w", p, err)
	}
	c.fill()
	owner := map[string]string{}
	for _, name := range c.Names() {
		s := c.Spheres[name]
		if !prefixRe.MatchString(s.Prefix) {
			return nil, fmt.Errorf("%s: sphere %s: prefix %q must be one to three capital letters, e.g. P", p, name, s.Prefix)
		}
		if other, ok := owner[s.Prefix]; ok {
			return nil, fmt.Errorf("%s: spheres %s and %s share the prefix %s; give one of them its own, e.g. prefix: U", p, other, name, s.Prefix)
		}
		owner[s.Prefix] = name
	}
	for i, h := range c.Hooks {
		if len(h.Run) == 0 {
			return nil, fmt.Errorf("%s: hook %d has no run", p, i+1)
		}
		for _, e := range h.Events {
			if !contains(HookEvents, e) {
				return nil, fmt.Errorf("%s: hook %d: event %q; expected one of %s", p, i+1, e, strings.Join(HookEvents, ", "))
			}
		}
	}
	return c, nil
}

func (c *Config) fill() {
	if c.Spheres == nil {
		c.Spheres = map[string]Sphere{}
	}
	for name, s := range c.Spheres {
		s.Root = Expand(s.Root)
		if s.Prefix == "" {
			s.Prefix = strings.ToUpper(name[:1])
		}
		s.Prefix = strings.ToUpper(s.Prefix)
		if s.VCS == "" {
			s.VCS = "jj"
		}
		c.Spheres[name] = s
	}
}

func (c *Config) File() string { return c.path }

func (c *Config) Names() []string {
	var out []string
	for n := range c.Spheres {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// AddSphere declares a sphere and writes the file.
func (c *Config) AddSphere(name string, s Sphere) error {
	if !sphereName.MatchString(name) {
		return fmt.Errorf("sphere name %q: use lower-case letters, digits, - or _, e.g. perso", name)
	}
	if s.Prefix == "" {
		s.Prefix = strings.ToUpper(name[:1])
	}
	for other, o := range c.Spheres {
		if other != name && o.Prefix == s.Prefix {
			return fmt.Errorf("sphere %s already uses the prefix %s; give --prefix, e.g. --prefix U", other, s.Prefix)
		}
	}
	c.Spheres[name] = s
	if err := os.MkdirAll(filepath.Dir(c.path), 0o755); err != nil {
		return err
	}
	raw := &Config{}
	if b, err := os.ReadFile(c.path); err == nil {
		if err := yaml.Unmarshal(b, raw); err != nil {
			return fmt.Errorf("%s: %w", c.path, err)
		}
	}
	if raw.Spheres == nil {
		raw.Spheres = map[string]Sphere{}
	}
	raw.Spheres[name] = s
	b, err := yaml.Marshal(raw)
	if err != nil {
		return err
	}
	return os.WriteFile(c.path, b, 0o644)
}

// Expand replaces a leading ~ with the home directory.
func Expand(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, strings.TrimPrefix(p, "~"))
	}
	return p
}

// SphereOfID finds the sphere whose prefix starts a note id (PN-0001 → perso).
func (c *Config) SphereOfID(id string) (string, bool) {
	id = strings.ToUpper(strings.TrimSpace(id))
	for _, name := range c.Names() {
		if strings.HasPrefix(id, c.Spheres[name].Prefix+"N-") {
			return name, true
		}
	}
	return "", false
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}
