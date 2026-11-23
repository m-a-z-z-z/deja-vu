# Mistral Vibe session format

## Store and files

Mistral Vibe (`mistralai/mistral-vibe`) stores every session under
`${VIBE_HOME:-~/.vibe}/logs/session` (`session_logging.save_dir` in `~/.vibe/config.toml`
can move it). `DEJA_VIBE_ROOT` replaces the store root for deja without affecting where
Vibe writes; `VIBE_HOME` and the extra roots under `DEJA_HOME_ROOTS` are honoured too.
Since 2.26 the CLI writes two layouts, and a reader has to hold both.

### Unified engine (the default)

One event-sourced store per session under `unified/<session_id>/`:

- `CURRENT`: JSON pointer to the live generation (`session_id`, `generation`,
  `snapshot_sequence`, `manifest_sha256`, `store_format_minor`).
- `meta.json`: `SessionMetadata` (`session_id`, `parent_session_id`, `title`,
  `start_time`, `end_time`, `bumped_at`, `environment.working_directory`,
  `origin_directory`).
- `generations/<gen>/manifest.json`: the files of that generation — `checkpoint`,
  `runtime_state` and `projection_state` records (`path`, `sha256`, optional ordered
  `chunks`), plus `recovery_journal_segment` and the `snapshot_sequence`.
- `projection-state.json`: the projection snapshot; its `snapshot.history.entries`
  hold the history. When the transcript was detached into the chunk pool, the
  document names it and the concatenated `chunks/<sha256>.json` arrays — each one a
  JSON array of entries — refill that list.
- `journal/<n>.jsonl`: append-only records after the snapshot; each `projection_delta`
  (and a `session_relocated`) carries ops — `append_entry`, `replace_entry` / `remove_entry`
  by entry `id`, `set_history_entries` — replayed over the snapshot, in `sequence` order,
  from `snapshot_sequence + 1` on.

A store minor a newer writer raised names a shape an older reader must not attempt; the
reader tracks `store_format_minor` and stays lenient about unknown fields otherwise.
Sub-agents leave no history of their own in this engine: the lead's transcript holds the
spawn, the wait and the answer.

### Legacy engine

`<session_prefix>_<date>_<time>_<id>/` (prefix `session` by default) with:

- `messages.jsonl`: one `{"role","content"}` message per line, `content` a string or an
  array of text blocks; saved at the end of each step and holding no timestamps.
- `meta.json`: the same `SessionMetadata` shape as the unified stores.
- `agents/<id>/`: a sub-agent's own session; deja skips these trees.

## Records

A replayed entry is typed by its `type`:

- `message`: `user` or (implicitly) `assistant` speech; the `text` blocks of `content`
  join into one message. The first user line seeds the title when `meta.json` omits it.
- `reasoning`: the model thinking out loud; kept out of the transcript, as in every other
  harness here.
- `effect`: a tool call. Its settled `display` (`settledVerb` / `settledMessage`, or
  `verb` / `message`) becomes a `tool-output` record and a tool-call count;
  `success: false` counts a tool error.
- `notice`, `callback`, `checkpoint`: UI and lifecycle records with no conversation in
  them.

Neither engine stamps the messages themselves, so read afterwards the turns are spread
over the session's own `start_time` … `end_time` span; an entry that carries
`createdAt` keeps its own time.

## Wiring and resume

- **Resume & handoff**: `deja resume` runs `vibe --resume <id>` in the recorded working
  directory; `deja handoff --to vibe --exec` runs `vibe -p <packet>`.
- **WriteBack** refuses: the store belongs to Vibe's own active runtime and its journal
  is digest-chained.
- **Hooks, MCP, skills and rules** are not wired for Vibe yet.

## Synthetic fixtures

- [`fixtures/registry/vibe/sessions/session_20261016_090000_8f3a2c/`](../../fixtures/registry/vibe/sessions/session_20261016_090000_8f3a2c/)
  (`messages.jsonl` beside `meta.json`) is the legacy conformer.
- [`fixtures/registry/vibe/unified/sess01m9q4/`](../../fixtures/registry/vibe/unified/sess01m9q4/)
  (`CURRENT`, `meta.json`, a generation manifest with its projection snapshot, and a
  journal whose deltas append an effect and a message) is the unified conformer.

**Last verified:** 2026-11-23 against mistralai/mistral-vibe 3.1.0
(`harness/runtimes/python/python/mistralai_vibe_local_harness/vibe/_storage.py`,
`vibe/core/session/session_index.py`, `session_loader.py`,
`vibe/cli-rust/src/server/types.rs`, `effect.rs`, `cli.rs`)
