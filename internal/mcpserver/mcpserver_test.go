package mcpserver

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/aclemen1/note-cli/internal/config"
	"github.com/aclemen1/note-cli/internal/store"
)

func TestServeOnlyGivenSpheres(t *testing.T) {
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
	ctx := context.Background()
	st, ct := mcp.NewInMemoryTransports()
	if _, err := New(cfgPath, []string{"perso"}).Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()

	tools, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, tl := range tools.Tools {
		names[tl.Name] = true
	}
	for _, want := range []string{"note_add", "note_ls", "note_show", "note_edit", "note_archive", "note_restore", "note_rm", "note_refs"} {
		if !names[want] {
			t.Errorf("missing tool %s", want)
		}
	}
	if names["setup_init"] || names["setup_tui"] || names["meta_skill"] {
		t.Error("setup and meta actions must not be served")
	}

	call := func(name string, args map[string]any) (bool, map[string]any) {
		res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			t.Fatal(err)
		}
		var env map[string]any
		_ = json.Unmarshal([]byte(res.Content[0].(*mcp.TextContent).Text), &env)
		return res.IsError, env
	}
	if isErr, env := call("note_add", map[string]any{"text": "Quota rétabli.", "ref": []string{"office:P-0030"}, "sphere": "perso"}); isErr {
		t.Fatalf("add: %v", env)
	} else if r := env["result"].(map[string]any); r["id"] != "PN-0001" || r["by"] != "agent:mcp" {
		t.Fatalf("add: %v", r)
	}
	if isErr, _ := call("note_add", map[string]any{"text": "x", "ref": []string{"office:U-1"}, "sphere": "pro"}); !isErr {
		t.Fatal("a sphere not served must be refused")
	}
	if isErr, _ := call("note_add", map[string]any{"text": "x", "sphere": "perso"}); !isErr {
		t.Fatal("a note without ref must be refused")
	}
	if isErr, env := call("note_ls", map[string]any{"ref": []string{"office:"}}); isErr || len(env["result"].(map[string]any)["items"].([]any)) != 1 {
		t.Fatalf("ls: %v", env)
	}
}
