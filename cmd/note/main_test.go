package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aclemen1/note-cli/internal/actions"
	"github.com/aclemen1/note-cli/internal/config"
	"github.com/aclemen1/note-cli/internal/spec"
)

type env struct {
	t   *testing.T
	dir string
	cfg string
}

func setup(t *testing.T) *env {
	t.Helper()
	dir := t.TempDir()
	e := &env{t: t, dir: dir, cfg: filepath.Join(dir, "config.yaml")}
	t.Setenv("NOTE_CONFIG", e.cfg)
	t.Setenv("NOTE_BY", "")
	loc, _ := time.LoadLocation("Europe/Zurich")
	now := time.Date(2026, 10, 9, 18, 30, 0, 0, loc)
	actions.SetClock(func() time.Time { return now })
	t.Cleanup(func() { actions.SetClock(nil) })
	e.ok("init", "--sphere", "perso", "--root", filepath.Join(dir, "perso"), "--vcs", "none")
	e.ok("init", "--sphere", "pro", "--root", filepath.Join(dir, "pro"), "--prefix", "U", "--vcs", "none")
	f, _ := os.OpenFile(e.cfg, os.O_APPEND|os.O_WRONLY, 0o644)
	f.WriteString("by: contact:AC\n")
	f.Close()
	return e
}

func (e *env) run(args ...string) (int, map[string]any, string) {
	var out, errw bytes.Buffer
	code := run(args, strings.NewReader(""), &out, &errw)
	var env map[string]any
	_ = json.Unmarshal(out.Bytes(), &env)
	return code, env, out.String() + errw.String()
}

func (e *env) ok(args ...string) map[string]any {
	e.t.Helper()
	code, env, raw := e.run(args...)
	if code != 0 {
		e.t.Fatalf("%v: exit %d: %s", args, code, raw)
	}
	r, _ := env["result"].(map[string]any)
	return r
}

func (e *env) fails(code int, args ...string) string {
	e.t.Helper()
	got, _, raw := e.run(args...)
	if got != code {
		e.t.Fatalf("%v: exit %d, want %d: %s", args, got, code, raw)
	}
	return raw
}

func ids(r map[string]any) []string {
	var out []string
	items, _ := r["items"].([]any)
	for _, it := range items {
		out = append(out, it.(map[string]any)["id"].(string))
	}
	return out
}

func TestAddNeedsSphereAndRef(t *testing.T) {
	e := setup(t)
	e.fails(spec.ExitUsage, "add", "texte", "--ref", "office:P-0030")
	e.fails(spec.ExitUsage, "add", "texte", "--sphere", "perso")
	e.fails(spec.ExitUsage, "add", "texte", "--ref", "P-0030", "--sphere", "perso")
	e.fails(spec.ExitUsage, "add", "  ", "--ref", "office:P-0030", "--sphere", "perso")
	r := e.ok("add", "Drive rapatrie 1,2 Go.", "--ref", "office:P-0030,contact:JMR", "--sphere", "perso")
	if r["id"] != "PN-0001" || r["by"] != "contact:AC" || r["sphere"] != "perso" {
		t.Fatalf("add: %v", r)
	}
	if refs := r["refs"].([]any); len(refs) != 2 {
		t.Fatalf("refs: %v", refs)
	}
}

func TestListReadsEverySphereNewestFirst(t *testing.T) {
	e := setup(t)
	e.ok("add", "ancienne", "--ref", "oj:RDIR-17", "--sphere", "pro", "--at", "2026-10-02")
	e.ok("add", "récente", "--ref", "office:P-0030", "--sphere", "perso")
	if got := ids(e.ok("ls")); strings.Join(got, ",") != "PN-0001,UN-0001" {
		t.Fatalf("ls: %v", got)
	}
	if got := ids(e.ok("ls", "--order", "oldest")); strings.Join(got, ",") != "UN-0001,PN-0001" {
		t.Fatalf("ls oldest: %v", got)
	}
	if got := ids(e.ok("ls", "--ref", "oj:")); strings.Join(got, ",") != "UN-0001" {
		t.Fatalf("ls prefix: %v", got)
	}
	if got := ids(e.ok("ls", "--ref", "oj:RDIR-1")); len(got) != 0 {
		t.Fatalf("a ref without : or / is exact: %v", got)
	}
	if got := ids(e.ok("ls", "--search", "RECENTE")); strings.Join(got, ",") != "PN-0001" {
		t.Fatalf("search folds case and accents: %v", got)
	}
	if got := ids(e.ok("ls", "--since", "3d")); strings.Join(got, ",") != "PN-0001" {
		t.Fatalf("since: %v", got)
	}
}

func TestSourceIsUnique(t *testing.T) {
	e := setup(t)
	e.ok("add", "un", "--ref", "oj:RDIR-17", "--sphere", "pro", "--source", "oj:RDIR-17#1")
	e.fails(spec.ExitConflict, "add", "deux", "--ref", "oj:RDIR-17", "--sphere", "pro", "--source", "oj:RDIR-17#1")
	r := e.ok("add", "deux", "--ref", "contact:JMR", "--sphere", "pro", "--source", "oj:RDIR-17#1", "--upsert")
	if r["id"] != "UN-0001" || r["body"] != "deux" || len(r["refs"].([]any)) != 2 {
		t.Fatalf("upsert: %v", r)
	}
	e.ok("add", "ailleurs", "--ref", "oj:RDIR-17", "--sphere", "perso", "--source", "oj:RDIR-17#1")
}

func TestEditKeepsOneRef(t *testing.T) {
	e := setup(t)
	e.ok("add", "texte", "--ref", "office:P-0030", "--sphere", "perso")
	e.fails(spec.ExitUsage, "edit", "PN-0001", "--rm-ref", "office:P-0030")
	r := e.ok("edit", "PN-0001", "--add-ref", "contact:JMR", "--rm-ref", "office:P-0030", "--text", "nouveau")
	if refs := r["refs"].([]any); len(refs) != 1 || refs[0] != "contact:JMR" || r["body"] != "nouveau" {
		t.Fatalf("edit: %v", r)
	}
	if log := r["log"].([]any); len(log) != 1 {
		t.Fatalf("log: %v", log)
	}
	r = e.ok("edit", "PN-0001", "--text", "nouveau")
	if log := r["log"].([]any); len(log) != 1 {
		t.Fatalf("an edit that changes nothing logs nothing: %v", log)
	}
}

func TestArchiveRestoreRemove(t *testing.T) {
	e := setup(t)
	e.ok("add", "a", "--ref", "office:P-1", "--sphere", "perso")
	e.ok("add", "b", "--ref", "office:P-1", "--sphere", "perso")
	e.ok("archive", "PN-0001")
	if got := ids(e.ok("ls")); strings.Join(got, ",") != "PN-0002" {
		t.Fatalf("archived hidden: %v", got)
	}
	if got := ids(e.ok("ls", "--state", "archived")); strings.Join(got, ",") != "PN-0001" {
		t.Fatalf("archived: %v", got)
	}
	e.ok("restore", "1", "--sphere", "perso")
	e.ok("rm", "PN-0002")
	e.fails(spec.ExitNotFound, "show", "PN-0002")
	if r := e.ok("add", "c", "--ref", "office:P-1", "--sphere", "perso"); r["id"] != "PN-0003" {
		t.Fatalf("the id of a removed note is never given again: %v", r["id"])
	}
}

func TestRefs(t *testing.T) {
	e := setup(t)
	e.ok("add", "a", "--ref", "office:P-1,contact:JMR", "--sphere", "perso")
	e.ok("add", "b", "--ref", "contact:JMR", "--sphere", "pro")
	_, env, _ := e.run("refs", "--prefix", "contact:")
	l := env["result"].([]any)
	if len(l) != 1 || l[0].(map[string]any)["count"].(float64) != 2 {
		t.Fatalf("refs: %v", l)
	}
}

func TestHooksFollowRefPrefix(t *testing.T) {
	e := setup(t)
	log := filepath.Join(e.dir, "hook.log")
	script := filepath.Join(e.dir, "hook.sh")
	os.WriteFile(script, []byte("#!/bin/sh\necho \"$NOTE_EVENT $NOTE_ID $NOTE_REFS\" >> "+log+"\n"), 0o755)
	f, _ := os.OpenFile(e.cfg, os.O_APPEND|os.O_WRONLY, 0o644)
	f.WriteString("hooks:\n  - events: [added, archived]\n    ref: \"oj:\"\n    run: [\"" + script + "\"]\n")
	f.Close()
	e.ok("add", "a", "--ref", "oj:RDIR-17", "--sphere", "pro")
	e.ok("add", "b", "--ref", "office:P-1", "--sphere", "pro")
	e.ok("archive", "UN-0001")
	e.ok("archive", "UN-0001")
	b, _ := os.ReadFile(log)
	if got := string(b); got != "added UN-0001 oj:RDIR-17\narchived UN-0001 oj:RDIR-17\n" {
		t.Fatalf("hooks: %q", got)
	}
}

func TestConfigRefusesUnknownHookEvent(t *testing.T) {
	e := setup(t)
	f, _ := os.OpenFile(e.cfg, os.O_APPEND|os.O_WRONLY, 0o644)
	f.WriteString("hooks:\n  - events: [done]\n    run: [\"true\"]\n")
	f.Close()
	if _, err := config.Load(""); err == nil {
		t.Fatal("an unknown event must be refused")
	}
}

func TestConfigCompleteByField(t *testing.T) {
	for _, line := range []string{"complete: [a, b]\n", "complete:\n  refs: [a, b]\n"} {
		e := setup(t)
		f, _ := os.OpenFile(e.cfg, os.O_APPEND|os.O_WRONLY, 0o644)
		f.WriteString(line)
		f.Close()
		cfg, err := config.Load("")
		if err != nil || strings.Join(cfg.Complete["refs"], ",") != "a,b" {
			t.Fatalf("%q: %v %v", line, cfg.Complete, err)
		}
	}
}
