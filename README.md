# coding-agent-wrapper

Easy distribution of organization defaults for coding agents.

Keep your team's settings, MCP servers, plugins, skills, permission rules
and guardrails in **one versioned git repo**. Developers run `acme claude`
instead of `claude`, and the current org defaults are applied on every
launch, without setup scripts and without writing to their local setup.

```
                    defaults pack (a git repo)
              ┌──────────────────────────────────────┐
              │ claude/                              │
              │   settings.json    policy.json       │
              │   mcp.json  env.json                 │
              │   plugin/  (skills, agents, ...)     │
              │   system-prompt.md                   │
              └──────────────┬───────────────────────┘
                             │ sync: git clone/pull, cached
┌─────────────┐       ┌──────┴───────┐        ┌──────────────┐
│  your CLI   │ ────▶ │ this library │ ─────▶ │  real agent  │
│ acme claude │ embed │Prepare + Exec│ inject │    claude    │
└─────────────┘       └──────────────┘  flags └──────────────┘
```

## Why

When a team adopts coding agents, every developer ends up with a different
setup: missing org skills, inconsistent permissions, no MCP servers, no
guardrails. The usual fixes don't scale:

- **"Run this setup script"**: it drifts, breaks on agent updates, and is
  hard to undo.
- **Dotfiles-style provisioning**: mutates `~/.claude`, fights the user, and
  silently rots.
- **Forking the agent**: unmaintainable.

The wrapper takes a different route: defaults live in a **git repo** (the
"defaults pack"), and a small **wrapper binary** applies them per launch
using the agent's own injection points.

- Defaults live in one repo. A commit updates every developer on their
  next launch; there is no rollout step and no machine drift.
- Org defaults merge under the developer's own settings, so personal and
  project choices win. Org requirements that must not be overridden go in
  a separate policy layer that only applies with `--enforce`
  ([docs/config-layers.md](docs/config-layers.md)).
- Nothing is written to `~/.claude` or to any project. Stop using the
  wrapper and the setup is stock again.
- The pack can be pinned to a branch, tag or commit, so every machine and
  CI job sees the same configuration. Cached packs keep launches working
  offline.
- Each agent has an adapter that translates the pack into its native
  injection points. Claude Code ships here; OpenCode and pi use the same
  interface.
- You compile your own binary with your CLI name and pack URL baked in.
  Developers install one internal tool and run `acme claude`.

## Getting started

### 1. Feel it with the reference CLI

```sh
go build -o wr ./cmd/wr

wr doctor                       # setup check: binaries found? pack state?
wr --pack ./testdata/pack doctor        # ...and the computed launch, with reasons
wr --pack ./testdata/pack claude        # run real Claude Code with the example pack
```

`testdata/pack` is a complete example org pack. `doctor` never runs anything.
It prints the binary, the exact arguments, injected env vars and a decision
trail.

### 2. Create your org pack

Copy `testdata/pack` into a new repo and edit. Every file is optional and
uses the format the agent natively understands:

```
version.json              metadata: {"name": "...", "version": "..."}
claude/
  settings.json           org defaults (users and projects always win)
  policy.json             locked layer, applied only with --enforce
  mcp.json                MCP servers added to the user's own
  env.json                environment variable defaults
  plugin/                 org skills, agents, commands, hooks
  system-prompt.md        appended to the agent's system prompt
```

### 3. Ship your wrapper

Embed the library; the binary is your org's front door:

```go
package main

import (
	"context"
	"os"

	wrapper "github.com/wejick/coding-agent-wrapper"
	"github.com/wejick/coding-agent-wrapper/adapters/claude"
	"github.com/wejick/coding-agent-wrapper/pack"
)

func main() {
	wrapper.Register(claude.New())
	err := wrapper.Run(context.Background(), wrapper.Options{
		Agent:   "claude",
		Args:    os.Args[1:],
		Pack:    pack.Git("github.com/acme/agent-defaults#pack", "v1"),
		Enforce: os.Getenv("ACME_ENFORCE") == "1",
	})
	if err != nil {
		os.Exit(1)
	}
}
```

Distribute it however your org already ships internal tools (brew, internal
npm/Go installs, MDM). On UNIX, `Run` execs in place: signals, exit codes and
terminal behavior are the agent's own. Employees type `acme claude`; the
pack URL and ref are baked in, so there is nothing for them to configure.

## Deep dives

| Doc | Contents |
| --- | --- |
| [docs/config-layers.md](docs/config-layers.md) | The layering model: how org defaults, policy, user settings, project settings and CLI flags combine; `settings.json` vs `policy.json`; env, MCP and plugin interaction |
| [docs/adapters.md](docs/adapters.md) | Supporting a new agent (OpenCode, pi, ...) behind the `Adapter` interface |
| [AGENTS.md](AGENTS.md) | Repository layout, design principles, build and test conventions |

## Status

Claude Code adapter is complete and tested against a real install.
OpenCode and pi adapters are next; the `Adapter` interface is stable for
them.
