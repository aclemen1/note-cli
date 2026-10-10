# note

Notes tied to objects: an information that serves the follow-up of a case, a
meeting item, a contact, with no date to meet and no action. One binary: CLI,
MCP server (`note mcp`), terminal interface (`note tui`) and agent skill
(`note skill install`).

```bash
go install ./cmd/note
note init --sphere perso --root ~/note/perso
note init --sphere pro --root ~/note/pro --prefix U
note add "Drive rapatrie 1,2 Go en double, laissé tel quel." --ref office:P-0030 --sphere perso
note ls --ref office:P-0030 --format text
note schema
```

## Store

One Markdown file per note in `<root>/<prefix>N-0001.md`: YAML front matter for
the fields, body for the text. Each change is committed (jj by default) under a
lock in `<root>/.note/lock`. An id is never given twice, even after `rm`
(`<root>/.note/last`).

| Field | Meaning |
|---|---|
| `refs` | objects the note belongs to, as `<tool>:<id>`; at least one |
| `source` | origin, as `<tool>:<id>`; one note per source and sphere |
| `by` | author: `--by`, else `$NOTE_BY`, else `agent:mcp` through MCP, else `by` of the config |
| `state` | `active`, `archived` |
| `created` | date of the entry (`--at` sets it) |
| `updated`, `log` | last change, and who changed what |

## Reading

Every sphere is read unless `--sphere` is given. `note ls` lists newest first
(`--order oldest` for a journal). Several `--ref` keep the notes citing all of
them; a ref ending with `:` or `/` is a prefix (`--ref office:`). `note refs`
lists the objects cited, with their count and last date.

## Writing from another tool

Another tool reads and writes its notes through the CLI, in JSON by default:

```bash
note add "<text>" --ref <tool>:<id> --sphere <sphere> [--source <tool>:<id> --upsert]
echo "<markdown>" | note add - --ref <tool>:<id> --sphere <sphere>
note ls --ref <tool>:<id>
```

## TUI

`note tui [--ref <tool>:<id>] [--sphere <sphere>] [--select <id>]`: the notes,
newest first, the detail of the selected one below. `--select` opens on one
note (an archived one in the view of every note); an unknown id opens as usual,
with a message. Every entry opens in a tuikit modal.

| Key | Action |
|---|---|
| `j` `k` `gg` `G` `ctrl+d` `ctrl+u` | move |
| `enter` / `esc` | full detail / back |
| `tab` | detail panel on / off |
| `J` `K` | scroll the detail |
| `1` `2` `3` | view: active, archived, all (`a`: active ↔ all) |
| `/` | filter on the text, the refs and the source |
| `f` / `F` | notes of one ref (completion) / every ref |
| `s` | sphere: every one, then each |
| `c` `n` | new note: sphere, refs, text, source |
| `E` | edit refs, text, source |
| `e` (`x`) / `u` | archive / restore |
| `space` | archive or restore |
| `#` | delete, after typing the id |
| `o` | run `open.run` with `{"sphere","note"}` on stdin |
| `r` | reload |
| `?` `q` | help, quit |

It reloads when a note file changes or on SIGUSR1, and restarts itself on a new
binary.

## Hooks

A hook runs a command after an event of a note. The event comes as JSON on
stdin (`event`, `sphere`, `previous_state`, `by`, `note`) and in the environment
(`NOTE_EVENT`, `NOTE_ID`, `NOTE_SPHERE`, `NOTE_STATE`, `NOTE_SOURCE`,
`NOTE_REFS`). Events: `added`, `edited`, `archived`, `restored`, `removed`.
An action that changes nothing fires nothing.

```yaml
hooks:
  - events: [added, edited]
    ref: "meeting:"            # notes with a ref that starts with this
    source: "mail:"            # notes whose source starts with this
    sphere: pro
    run: ["~/bin/meeting-notes"]   # argv; a failure is a warning, never an error
```

## Configuration

`~/.config/note/config.yaml` (or `$NOTE_CONFIG`, `--config`):

```yaml
by: contact:AC                 # author of the owner's notes
spheres:
  perso: {root: ~/note/perso, prefix: P, vcs: jj}
  pro: {root: ~/note/pro, prefix: U, vcs: jj}
open:
  run: ["~/bin/note-open"]     # {"sphere","note"} on stdin
complete:
  refs: [cases, people]        # sources of tuikit's refs.yaml for the refs fields and the f filter
```

The TUI completes refs from the sources named by `complete.refs`, declared once in
tuikit's `~/.config/tuikit/refs.yaml` (`$TUIKIT_REFS`), then from the refs
already cited.

| Variable | Effect |
|---|---|
| `NOTE_BY` | author of the call (`agent:…` for an agent) |
| `NOTE_CONFIG` | configuration file |
