# Example defaults pack

A complete, working defaults pack for the Claude Code adapter. Copy this
directory into a new git repo and edit the files; that repo becomes your
organization's defaults pack.

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

Every file is optional. A `claude/` directory with no files launches the
agent unchanged.

## Try it from this repository

```sh
go build -o wr ./cmd/wr
./wr --pack ./examples/pack doctor
./wr --pack ./examples/pack claude
```

## Use it as your organization's pack

Copy the contents into a new repo, push it, and point your wrapper binary
at it:

```go
pack.Git("github.com/acme/agent-defaults", "v1")
```

The full layering model, including what overrides what, is in
[docs/config-layers.md](../../docs/config-layers.md).
