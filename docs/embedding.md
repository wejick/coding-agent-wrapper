# Embedding the library

`wrapper.Run` is enough for a wrapper that launches agents with the pack's
cached copy. This page covers the two calls an embedding binary needs when
it does more: refreshing the pack on its own schedule, and showing users
what the pack provides.

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
