// Package hooks runs the commands of the configuration on the events of a
// note, so that other tools follow it without note knowing them.
package hooks

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/aclemen1/note-cli/internal/config"
	"github.com/aclemen1/note-cli/internal/store"
)

// Event is what a hook reads on its standard input.
type Event struct {
	Event         string      `json:"event"`
	Sphere        string      `json:"sphere"`
	PreviousState string      `json:"previous_state,omitempty"`
	By            string      `json:"by"`
	Note          *store.Note `json:"note"`
}

func matches(h config.Hook, e Event) bool {
	if len(h.Events) > 0 {
		ok := false
		for _, x := range h.Events {
			if x == e.Event {
				ok = true
			}
		}
		if !ok {
			return false
		}
	}
	if h.Sphere != "" && h.Sphere != e.Sphere {
		return false
	}
	if h.Source != "" && !strings.HasPrefix(e.Note.Source, h.Source) {
		return false
	}
	if h.Ref != "" {
		ok := false
		for _, r := range e.Note.Refs {
			if strings.HasPrefix(r, h.Ref) {
				ok = true
			}
		}
		if !ok {
			return false
		}
	}
	return true
}

// Fire runs every matching hook, one after the other; a failure is a warning, never an error of the action.
func Fire(cfg *config.Config, e Event, warn func(string)) {
	for _, h := range cfg.Hooks {
		if !matches(h, e) || len(h.Run) == 0 {
			continue
		}
		if err := run(h, e); err != nil && warn != nil {
			warn(fmt.Sprintf("hook %s on %s %s: %v", h.Run[0], e.Event, e.Note.ID, err))
		}
	}
}

func run(h config.Hook, e Event) error {
	timeout := 30 * time.Second
	if h.Timeout != "" {
		d, err := time.ParseDuration(h.Timeout)
		if err != nil {
			return fmt.Errorf("timeout %q: %v", h.Timeout, err)
		}
		timeout = d
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	argv := make([]string, len(h.Run))
	for i, a := range h.Run {
		argv[i] = config.Expand(a)
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	in, _ := json.Marshal(e)
	cmd.Stdin = bytes.NewReader(in)
	cmd.Env = append(os.Environ(),
		"NOTE_EVENT="+e.Event, "NOTE_ID="+e.Note.ID, "NOTE_SPHERE="+e.Sphere,
		"NOTE_STATE="+e.Note.State, "NOTE_SOURCE="+e.Note.Source, "NOTE_REFS="+strings.Join(e.Note.Refs, ","))
	if home, err := os.UserHomeDir(); err == nil {
		cmd.Dir = home
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
