# Supporting a new agent

The wrapper is agent-agnostic: each agent gets an `Adapter` that translates
one shared concept, a defaults pack, into that agent's own launch-time
injection points. Adding one is a package in `adapters/<name>/`, an
interface implementation, and a `Register` call in the consumer binary.

## The interface

```go
type Adapter interface {
	// Name is the subcommand users type, e.g. "opencode".
	Name() string
	// Locate returns the path of the real agent binary, skipping the
	// wrapper itself on PATH.
	Locate() (string, error)
	// Build computes the launch for the given pack.
	Build(ctx context.Context, p *pack.Pack, o BuildOptions) (*Launch, error)
}
```

`Build` receives the loaded pack (`p.Subdir("opencode", ...)` gives you the
pack's `opencode/` directory) and returns everything needed to start the
agent: resolved binary, final argv, full environment, generated files, and a
human-readable `Notes` trail. `Run` = `Prepare` + `Exec`; adapters only own
`Build`.

## Rules every adapter should follow

1. Apply everything at launch time. Translate pack files into flags, env
   vars and generated config files the agent natively accepts. Never write
   to the user's config directories.
2. Merge org defaults under what the user already has. Read the user's
   config, merge the pack beneath it so the higher layer wins per key and
   additive lists union: if the pack sets `"includeCoAuthoredBy": false`
   and the user set it to `true`, `true` survives. The locked
   `policy.json` layer is the only thing allowed on top, and the adapter
   applies it unless `o.SkipPolicy` is set. See
   [config-layers.md](config-layers.md) for the
   model the Claude adapter implements.
3. Put user args last. Append `o.Args` after injected flags so explicit
   user flags still apply.
4. Never recurse into the wrapper. Resolve the real binary with
   `wrapper.ResolveBinary(name, nil)`; it skips the wrapper's own
   executable on PATH.
5. Explain every decision. A merged layer, a skipped env var, an ignored
   policy: each becomes a line in `Launch.Notes`. `doctor` prints them;
   they are the primary debugging surface.
6. Know the agent's injection points precisely. If a flag doesn't exist
   on older agent versions, that's acceptable (the agent's own error
   surfaces it), but prefer the stable ones and document them in the
   package doc comment.

## Mapping agents to injection points

| Concern | Claude Code (shipped) | OpenCode (planned) | pi (planned) |
| --- | --- | --- | --- |
| Settings/config | `--settings <merged file>` | `OPENCODE_CONFIG` env → generated config | config paths |
| MCP servers | `--mcp-config` (+ `--strict-mcp-config`) | `mcp` key in config | extensions/config |
| Env vars | process env (+ `env` settings key) | process env / provider config | process env |
| Skills, agents, commands | `--plugin-dir <dir>` (session-scoped plugin) | `.opencode/` dirs on disk | extensions |
| System prompt | `--append-system-prompt-file` | rules/instructions config | system prompt config |
| Policy layer | merged on top, passed via `--settings` | merged into generated config | merged into generated config |

Agents without a native "extra config layer" mechanism (like Claude's
`--settings`) need the generated-file approach: read the user's config,
merge the pack under it, write to a cache path, and point the agent at it
via its config-path env var or flag. The Claude adapter's
`writeMergedSettings` (atomic write, cache dir, deterministic path) is the
reference pattern.

## Testing recipe

Adapters are tested without any agent installed:

- Put a fake executable named after the agent in a temp dir and `t.Setenv("PATH", dir)`, and `Locate` resolves it.
- Set `t.Setenv("HOME", tmp)` and `a.ProjectDir = tmp2` to control every settings layer.
- Assert on the computed `Launch`: args order, merged-file contents, env diff, notes.
- See `adapters/claude/claude_test.go` for the full pattern, including the
  project-layering and policy tests.

Register the adapter from the *consumer's* binary
(`wrapper.Register(opencode.New())`), keeping `wrapper` itself free of
agent imports; binaries compile in only the agents they launch.
