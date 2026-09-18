# AGENTS.md

Guidance for coding agents (and humans) working in this repository.

## What this repo is

A Go library that turns coding agents (Claude Code, OpenCode, pi) into
org-managed launchables: a thin launcher applies an organization's defaults
pack (versioned config, MCP servers, plugins, skills, env and policy) at
launch time, without modifying user or project configuration.
`cmd/wr` is a reference CLI; the library is the product.

## Layout

```
wrapper.go              public API: Options, Launch, Prepare, Run
adapter.go              Adapter interface + registry (Register, Lookup, Agents)
resolve.go              PATH resolution that never recurses into the wrapper
exec_unix.go            in-place exec (syscall.Exec), the happy path on UNIX
exec_other.go           fork/exec fallback for non-UNIX
pack/
  pack.go               pack loading, Source interface (Local/Git), metadata
  git.go                git-backed distribution: clone, refresh, ref pinning,
                        offline fallback, #subdir roots
  merge.go              semantic JSON deep-merge (union arrays) + env merging
adapters/claude/
  claude.go             Claude Code adapter: builds the launch from the pack
  claude_test.go        asserts on computed launches and merged settings
e2e_test.go            black-box tests: build the wr binary, drive it with a
                       fake agent, pin the exec model and anti-recursion
cmd/wr/main.go          reference CLI: run, doctor, sync, agents
examples/pack/          complete example org pack (also used by tests)
docs/
  config-layers.md      the configuration layering model (canonical reference)
  adapters.md           how to add an agent adapter
```

## Commands

```sh
go build ./...          # compile
go build -o wr ./cmd/wr # build the reference CLI
go test ./...           # all tests; no network required
gofmt -l .              # must print nothing
go vet ./...            # must pass
```

## Design principles

- The library is the product and the CLIs are its consumers. Anything
  org-specific (names, pack URLs, enforcement defaults) belongs in the
  embedding binary, not in `wrapper` or `pack`.
- Everything is applied at launch time and nothing is mutated. Pack files
  become flags, env vars and generated files the agent natively accepts.
  Never write to `~/.claude`, `.claude/` or any user or project config.
  Generated artifacts live under the user's cache dir and are rewritten
  atomically on each launch.
- The layer order is fixed. The pack's `claude/settings.json` is the
  lowest layer, followed by user, project and local-project settings,
  with `policy.json` on top when `Enforce` is set: if the pack ships
  `"model": "sonnet"` and the user set `"model": "opus"`, the merged
  settings contain `opus`. Higher layers win per key, and permission
  lists union. `docs/config-layers.md` is the canonical description.
- Launches must be deterministic and inspectable. `Launch.Notes` records
  each decision the wrapper makes, `doctor` surfaces those notes, pack
  refs are pinnable, and cached packs keep working offline while reporting
  that they are stale.
- Keep the module dependency-free: stdlib only. Distribution shells out to
  the `git` binary rather than linking a git library, so the user's SSH
  keys and credential helpers apply unchanged.
- On UNIX, exec the agent in place with `syscall.Exec` so signals, exit
  codes and TTY behavior are the agent's own. Keep the non-UNIX fallback
  compiling.
- The wrapper must never recurse into itself. Binary resolution skips the
  running executable (`resolve.go`), and tests must keep proving it.

## Documentation style

These rules apply to the README, docs/, code comments, commit messages and
review replies.

- Lead with the mechanics: what happens, in order, with a concrete
  example. Say which file is read first, what it overrides, and which
  value wins.
- A one-line mnemonic is allowed only at the end, after the explanation.
  Never open with it and never let it replace the explanation.
- No slogans and no short "wise" sentences ("One repo distributes
  everything."). They conclude without explaining anything.
- Never use contrastive redefinitions: "It's not X, it's Y", "Not X, but
  Y", "X isn't the point, Y is". When two things differ, state both
  plainly: "The slowness comes from the architecture, not the network."
- Banned shapes: "Here's the thing", "the real issue", "the key insight",
  "At its core", "when it comes to", "at the end of the day", "What this
  looks like in practice", "No X. No Y. Just Z.", "The good news is",
  "The catch is", "Let me be clear", "It's worth noting", restating the
  question, and "First / Second / Finally" when nobody asked for steps.
- No em dashes. Use a comma, a period, or parentheses.
- No closing offers: "Want me to...", "I hope this helps", "Let me know
  if...".
- Match depth to purpose. Overview and value sections stay short; the
  detailed walkthrough with examples belongs in docs/.

## Testing conventions

- Never run a real agent in tests. Fake binaries are files with +x in a
  temp dir on PATH (`fakeBinary` in `adapters/claude/claude_test.go`).
- Control every settings layer: `t.Setenv("HOME", ...)`,
  `Adapter.ProjectDir`, `Adapter.UserSettingsPath`, `Adapter.CacheDir`.
- Git sync tests use throwaway local repositories as origins; no network.
- Assert on computed `Launch` values (args order, merged JSON, env diff,
  notes), not on incidental formatting.
- End-to-end tests (`e2e_test.go`) build the reference CLI and run it as a
  black box: exit-code passthrough, SIGTERM reaching the agent directly,
  no recursion when the wrapper sits on PATH as the agent name, doctor and
  sync behavior.

## Conventions

- Module path `github.com/wejick/coding-agent-wrapper` must match the repo;
  imports are not aliased except `wrapper` for the root package.
- Notes and doctor output are user-facing: sentence case, concrete paths,
  no jargon.
- Public API changes update `README.md` and the relevant `docs/` file in
  the same commit.
