# Example defaults pack

A complete, working defaults pack for the Claude Code and OpenCode
adapters. Copy this directory into a new git repo and edit the files; that
repo becomes your organization's defaults pack.

## What each file does

- `version.json`: pack metadata, shown by `wr doctor` and `wr sync`.
- `claude/settings.json`: org defaults. Merged under the developer's user
  and project settings, so their choices win per key.
- `claude/policy.json`: locked layer, applied over everything the wrapper
  controls by default; `--no-policy` skips it for a launch.
- `claude/mcp.json`: MCP servers added to the developer's own.
- `claude/env.json`: environment variable defaults; in policy mode (the
  default) org values force-replace existing ones. This is also where the
  org's model gateway goes, for example `ANTHROPIC_BASE_URL` pointing at
  your LLM proxy (see `docs/config-layers.md`).
- `claude/plugin/`: an org plugin (skills, commands, hooks) loaded with
  `--plugin-dir`.
- `claude/system-prompt.md`: appended to the agent's system prompt.
- `opencode/settings.json`: org defaults in OpenCode config shape
  (permissions, MCP servers, runtime options). Merged under the
  developer's global config and their project `opencode.json`, so their
  choices win per key.
- `opencode/policy.json`: locked layer, passed to OpenCode above project
  config via `OPENCODE_CONFIG_CONTENT`; `--no-policy` skips it for a
  launch.
- `opencode/env.json`: environment variable defaults, same rules as the
  Claude adapter.
- `opencode/config/`: org agents, commands, plugins and skills, loaded
  via `OPENCODE_CONFIG_DIR` like a project `.opencode` directory.

Every file is optional. A `claude/` or `opencode/` directory with no files
launches that agent unchanged.

## Try it from this repository

```sh
go build -o wr ./cmd/wr
./wr --pack ./examples/pack doctor
./wr --pack ./examples/pack claude
./wr --pack ./examples/pack opencode
```

## Use it as your organization's pack

Copy the contents into a new repo, push it, and point your wrapper binary
at it:

```go
pack.Git("github.com/acme/agent-defaults", "v1")
```

The full layering model, including what overrides what, is in
[docs/config-layers.md](../../docs/config-layers.md).
