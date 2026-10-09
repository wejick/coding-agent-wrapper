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
a refresh that fails falls back to the cached copy. With `Fetched` set, `Prepare` loads the
pack from `res.Dir` and does not fetch again. The returned launch then
describes that fetch:

- `Launch.Source` ends in `(git)` when `res` came from a successful
  refresh, and in `(cache)` when it came from the cache.
- `Launch.Notes` includes `res.Notes`, such as `refresh failed (...);
  using cached pack` when the remote could not be reached.

`Pack` is still required, because `Launch.Source` uses its `Describe`
label. `Options.Refresh` is ignored when `Fetched` is set.

## Listing the skills a pack provides

Adapters that implement `wrapper.SkillLister` can list the org skills the
pack gives their agent, read from the same directory the launch loads:

| Agent | Skills directory |
| --- | --- |
| Claude Code | `claude/plugin/skills/<name>/SKILL.md` |
| OpenCode | `opencode/config/skills/<name>/SKILL.md` |

```go
p, err := pack.Load(res.Dir, src.Describe())
if err != nil {
	return err
}
a, err := wrapper.Lookup("claude")
if err != nil {
	return err
}
if sl, ok := a.(wrapper.SkillLister); ok {
	skills, err := sl.Skills(p)
	if err != nil {
		return err
	}
	for _, s := range skills {
		fmt.Printf("%s: %s (%s)\n", s.Name, s.Description, s.Path)
	}
}
```

Each `pack.Skill` carries the `name` and `description` from the
`SKILL.md` frontmatter and the path of the file. `Name` or `Description`
is empty when the frontmatter does not set it, so a pack's CI can check
that every skill has both. Skill directories without a `SKILL.md` are
skipped, and an agent whose pack directory has no skills returns an empty
list. `wr doctor` prints the list under each agent's launch as `skill:`
lines, and `wr doctor --json` includes it as `skills`.
