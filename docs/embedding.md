# Embedding the library

`wrapper.Run` is enough for a wrapper that launches agents with the pack's
cached copy. It also checks the tools the pack requires and prints a note
for each problem; [tools.md](tools.md) covers that check and the setup
command that installs them. This page covers what an embedding binary
needs when it does more: refreshing the pack on its own schedule, showing
users what the pack provides, and running an agent headless as a batch
step.

## Refreshing the pack on your own schedule

The library has no built-in staleness policy. `Options.Refresh` either
refreshes on every launch or never. A binary that wants "refresh at most
hourly" fetches the pack itself and hands the result to `Prepare` or `Run`
through `Options.Fetched`:

```go
src := pack.Git("github.com/acme/agent-defaults#pack", "main")
res, err := src.Fetch(ctx, pack.FetchOptions{})
if err == nil && pack.Stale(res.Dir, time.Hour) {
	res, err = src.Fetch(ctx, pack.FetchOptions{Refresh: true})
}
if err != nil {
	return err
}
err = wrapper.Run(ctx, wrapper.Options{
	Agent:   "claude",
	Args:    os.Args[1:],
	Pack:    src,
	Fetched: res,
})
```

The first fetch uses the cache (or clones on first use). When the
checkout last synced more than an hour ago, the second fetch refreshes it;
a refresh that fails falls back to the cached copy. A failed refresh does
not update the sync time, so while the remote is unreachable every launch
retries the refresh and waits for git to give up. Pass a `ctx` with a
timeout to the refreshing fetch if that wait matters. With `Fetched` set, `Prepare` loads the
pack from `res.Dir` and does not fetch again. The returned launch then
describes that fetch:

- `Launch.Source` ends in `res.From`: `(git)` after a clone or a
  successful refresh, `(cache)` when the cached copy was used (including
  after a failed refresh), and `(local)` for `pack.Local`.
- `Launch.Notes` includes `res.Notes`, such as `refresh failed (...);
  using cached pack` when the remote could not be reached.

`Pack` is still required, because `Launch.Source` uses its `Describe`
label. `Options.Refresh` is ignored when `Fetched` is set, and the launch
notes record that.

## Listing the skills a pack provides

`Adapter.Skills(p)` returns the agent's view of the skills in a pack, or
nil when the adapter has no skill support. Its `List` method returns the
org skills, read from the same directories the launch loads:

| Agent | Skills directory |
| --- | --- |
| Claude Code | `claude/plugin/skills/**/SKILL.md` |
| OpenCode | `opencode/config/{skill,skills}/**/SKILL.md` |

Skills may be grouped in subdirectories, and symlinked skill directories
are followed, so one skill can be shared between both agents by linking
it. The Claude adapter does not list extra skill paths declared in the
plugin's `plugin.json`.

```go
p, err := pack.Load(res.Dir, src.Describe())
if err != nil {
	return err
}
a, err := wrapper.Lookup("claude")
if err != nil {
	return err
}
if skills := a.Skills(p); skills != nil {
	list, err := skills.List()
	if err != nil {
		return err
	}
	for _, s := range list {
		fmt.Printf("%s: %s (%s)\n", s.Name, s.Description, s.Path)
	}
}
```

Each `pack.Skill` carries the `name` and `description` from the
`SKILL.md` frontmatter and the path of the file. `Name` or `Description`
is empty when the frontmatter does not set it, so a pack's CI can check
that every skill has both. An agent whose pack directory has no skills
returns an empty list. `wr doctor` prints the list under each agent's launch as `skill:`
lines, and `wr doctor --json` includes it as `skills`.

## Running an agent headless

A batch job (a repo audit, a review bot, a scheduled task) gives the agent
one task, runs it against a given repository, waits, and reads the result.
Set `Options.Dir` to the repository and `Options.Headless` to the task, then
start the launch with `Launch.RunChild`:

```go
// Ctrl-C or SIGTERM cancels ctx, which stops the current run and the loop.
ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
defer stop()
for _, repo := range []string{"/src/page", "/src/sky"} {
	launch, err := wrapper.Prepare(ctx, wrapper.Options{
		Agent: "claude",
		Pack:  src,
		Dir:   repo,
		Headless: &wrapper.Headless{
			Prompt:     "/audit:readiness " + repo,
			Model:      "sonnet",
			Effort:     "medium",
			Unattended: true,
		},
	})
	if err != nil {
		return err
	}
	var out bytes.Buffer
	code, err := launch.RunChild(ctx, wrapper.RunOptions{Stdout: &out, Stderr: os.Stderr})
	if err != nil {
		return err
	}
	fmt.Printf("%s: agent exited %d\n", repo, code)
}
```

`Prepare` makes `Dir` absolute, fails when it is not a directory, and
stores it in `Launch.Dir`. The adapter reads project config from `Dir`
instead of the current directory, so each run gets that repository's own
`.claude/settings.json` and `settings.local.json` under the same pack,
user settings and policy as an interactive launch. With `Dir` empty the
launch uses the current directory, as before.

The Claude adapter turns `Headless` into flags placed after the pack's
flags and before `Options.Args`, and records one `headless:` note per
field. For `/src/page` above, the arguments end in:

```
-p "/audit:readiness /src/page" --permission-mode bypassPermissions --model sonnet --effort medium
```

| Field | Claude Code flag |
| --- | --- |
| `Prompt` (required) | `-p <prompt>` |
| `Unattended` | `--permission-mode bypassPermissions` |
| `Model` | `--model <model>` |
| `Effort` | `--effort <effort>` |

A settings layer that sets `permissions.disableBypassPermissionsMode` to
`"disable"` still applies and keeps Claude Code out of bypass mode, so
`Unattended` has no effect under it; the unattended note says so. The
prompt is passed as a bare argument, so the adapter rejects a prompt
that starts with `-`. Flags such as `--output-format json` go in
`Options.Args`.
The OpenCode adapter has no headless mapping yet and returns
`wrapper.ErrHeadlessUnsupported`; check for it with `errors.Is`.

`RunChild` starts the agent as a child process and waits for it:

1. It runs `Launch.Binary` with `Launch.Args` in `Launch.Dir`. The
   environment is `RunOptions.Env` when set, else `Launch.Env`, else
   `os.Environ()`.
2. Stdin is empty unless `RunOptions.Stdin` is set, because Claude Code in
   print mode reads piped stdin as extra prompt text. Stdout and stderr
   are discarded unless set.
3. SIGINT and SIGTERM sent to your process while the child runs are
   forwarded to the child (only interrupt off UNIX). Your process does not
   exit on them; it waits for the child. To stop a loop of runs as well,
   derive `ctx` from `signal.NotifyContext` as in the example. The child
   shares your process group, so a Ctrl-C typed in a terminal reaches it
   twice: once from the terminal and once forwarded.
4. When `ctx` is done, the child gets SIGTERM and is killed if it is still
   running 10 seconds later (off UNIX it is killed at once).
5. It returns the child's exit code. A child that could not start or was
   killed by a signal returns `-1` and an error, which wraps `ctx.Err()`
   when `ctx` was done. A child that exits on its own after `ctx` is done
   returns its exit code and `ctx.Err()`. A failing `Stdout` or `Stderr`
   writer, or output that a process left behind by the child still holds
   open 10 seconds after the child exits, returns the exit code with an
   error.

`Launch.Exec` and `wrapper.Run` also start the agent in `Launch.Dir` when
it is set, so an interactive launch with `Dir` runs where its project
settings came from.
