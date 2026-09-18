# Configuration layering

How the wrapper combines an organization's defaults with everything a user
and their projects already have. The short version:

> **The wrapper inserts two layers into Claude Code's existing stack (org
> defaults at the very bottom, opt-in org policy at the very top) and
> rebuilds the merge itself, so the result is deterministic and inspectable.
> Nothing on disk is modified.**

## The full stack

Highest wins per key:

```
┌──────────────────────────────────────────────────────┐
│ Enterprise managed settings (managed-settings.json)  │  installed by IT,
│                                                      │  above everything,
│                                                      │  out of our reach
├──────────────────────────────────────────────────────┤
│ Your own CLI flags (--model opus, --strict-mcp, ...) │  explicit intent,
│                                                      │  passes through
├──────────────────────────────────────────────────────┤
│ pack: claude/policy.json                (--enforce)  │  ORG LOCKED LAYER
│                                                      │  user cannot override
├──────────────────────────────────────────────────────┤
│ <project>/.claude/settings.local.json                │  native layers,
│ <project>/.claude/settings.json                      │  unchanged:
│ ~/.claude/settings.json                 (user)       │  personal > project
├──────────────────────────────────────────────────────┤
│ pack: claude/settings.json              (org default)│  ORG DEFAULTS
│                                                      │  lowest, pure baseline
├──────────────────────────────────────────────────────┤
│ Claude Code built-in defaults                        │
└──────────────────────────────────────────────────────┘
```

Layers 1–2 and 4 are exactly the native Claude Code behavior you already
have. The wrapper's job is layers 3 and 5, plus rebuilding the merge of
layer 4 and the org layers into a single generated file. A `--settings`
file passed on the command line would otherwise sit above the user and
project layers and silently outrank them.

At launch, the wrapper reads and applies files in this order:

1. It starts from the pack's `claude/settings.json` (the org defaults).
2. It merges in `~/.claude/settings.json`, and the user's value wins per
   key: if the org ships `"model": "sonnet"` and the user has
   `"model": "opus"`, the merged file contains `opus`.
3. It merges in `<project>/.claude/settings.json`, then
   `<project>/.claude/settings.local.json`. These sit above the user's
   settings, matching Claude Code's native precedence.
4. With `--enforce`, it applies `policy.json` into the file last, so
   neither the user nor a project can override those values.
5. It writes the result to a cache file and starts `claude` with
   `--settings <that file>`. Claude Code applies that file at the
   command-line tier, above everything except IT-managed settings and the
   flags you passed yourself.

Short version: org defaults lose to everything you already had, and org
policy beats everything except IT-managed settings and your own flags.

## Which file wins: a worked example

`pack/claude/settings.json` (org defaults):

```json
{
  "model": "claude-sonnet-4-5",
  "includeCoAuthoredBy": false,
  "permissions": { "allow": ["Bash(git status)", "Bash(git diff:*)"] }
}
```

`~/.claude/settings.json` (user):

```json
{
  "model": "claude-opus-4-1",
  "permissions": { "allow": ["WebSearch"] }
}
```

`myproject/.claude/settings.json` (project):

```json
{
  "model": "claude-haiku-4-5",
  "permissions": { "allow": ["Bash(npm run *)"] }
}
```

Result while running `wr claude` inside `myproject`:

| Key                     | Winner                  | Why                                    |
| ----------------------- | ----------------------- | -------------------------------------- |
| `model`                 | `claude-haiku-4-5`      | project > user > org defaults          |
| `includeCoAuthoredBy`   | `false`                 | only the org layer defines it          |
| `permissions.allow`     | all **five** entries    | lists **union** across every layer     |

The user's `opus` and the org's `sonnet` both lose to the project's
`haiku`; org defaults never outrank anything that existed before them.

## Merge rules (precise)

Applied at every layer boundary, by the wrapper (not by the agent):

1. **Objects** merge recursively, key by key.
2. **Scalars and most arrays**: the higher layer wins outright.
3. **Permission lists union**: `permissions.allow`, `permissions.deny`,
   `permissions.ask`, `permissions.additionalDirectories`. Lower-layer
   entries come first, duplicates removed, order stable. An org allowlist
   and a personal allowlist coexist; neither clobbers the other.
4. **`null` never erases**: an overlay `null` keeps the lower layer's value.

## `policy.json` vs `settings.json`

|                       | `settings.json` (defaults)   | `policy.json` (locked)         |
| --------------------- | ---------------------------- | ------------------------------ |
| Position in the stack | bottom                       | top                            |
| User can override?    | yes, per key                 | **no**                         |
| Applied               | always                       | only with `--enforce`          |
| Intended contents     | conveniences, allowlists, model defaults | guardrails: deny rules on secrets, audit settings, required endpoints |
| Without `--enforce`   | applied                      | ignored (doctor notes it)      |

Guideline: if a user would reasonably want to change it, it belongs in
`settings.json`. If the org requires it, it belongs in `policy.json`, and
people run with `--enforce` (typically baked into the org's wrapper binary
or `WRAPPER_ENFORCE=1`).

## Environment variables

Two distinct mechanisms, different rules:

| Source                          | Behavior                                          |
| ------------------------------- | ------------------------------------------------- |
| `settings.json`/`policy.json` → `env` key | merged like any other settings key (higher layer wins per variable); Claude Code applies it to its own tools |
| pack `claude/env.json`          | injected into the process environment at launch   |

For `env.json`: **your existing environment wins**. Org values are
defaults, so `DISABLE_TELEMETRY=custom` already set in your shell stays.
With `--enforce`, org values **force-replace** existing ones. Every
decision is recorded in the launch notes (`wr doctor`).

## MCP servers

- `claude/mcp.json` is passed via `--mcp-config`: org servers are **added**
  to whatever the user already has.
- `--strict-mcp-config` flips this: org servers **replace** the user's
  entirely (kiosk/CI scenarios).
- Name collisions between org and user servers are resolved by the agent;
  orgs should namespace their server names (`acme-handbook`, not `fetch`).

## Plugins, skills, commands

`claude/plugin/` is passed via `--plugin-dir`, loaded as a session-scoped
plugin. The user's own installed plugins, skills and commands are untouched
and keep working. Name your plugin after the org (`acme-defaults`) so both
can coexist.

## How this interacts with running vanilla `claude`

- Vanilla runs still work and behave as always: native layers only, no org
  anything. The wrapper is per-invocation, opt-in by using `wr` at all.
- Nothing the wrapper does persists: no file in `~/.claude` or any project
  is ever modified. Exit the wrapper habit and the org layers disappear.
- The one generated artifact is the merged settings file, rewritten on
  every launch under
  `~/Library/Caches/coding-agent-wrapper/merge/claude/` (macOS;
  `~/.cache/coding-agent-wrapper/...` on Linux). The filename carries a
  hash of the content (`settings-<hash>.json`), so concurrent launches
  with different packs can never overwrite each other's file; stale files
  are pruned after a month. **Read the exact path from `wr doctor` when
  debugging**: it is exactly what Claude Code will see.

## Inspecting the layering

```sh
wr doctor            # per-agent: computed args, injected env, notes for every layer decision
wr doctor --json     # same, machine-readable
```

The `launch:` section of `doctor` shows a line like:

```
settings layers (low → high): org defaults < user /Users/me/.claude/settings.json < project /work/app/.claude/settings.json < org policy (locked)
```

## Known edges

- **IT-managed settings** (`managed-settings.json`) sit above everything
  the wrapper controls, by design. The wrapper never fights enterprise
  policy.
- **Your explicit flags win.** `wr claude --model opus` overrides the
  model from every layer, because real argv flags are processed after the
  generated `--settings` file.
- **The wrapper recomputes the merge on every launch** instead of relying
  on the agent's internal precedence. If the agent changes its own
  ordering tomorrow, the org layering you configured keeps behaving as
  documented here.
