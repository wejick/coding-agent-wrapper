# Required tools

A pack can declare the command-line tools its skills, hooks and MCP
servers expect on the user's machine. The wrapper checks them at every
launch and reports problems as notes, and a setup command (`wr init` in the
reference CLI) installs what is missing after asking the user.

## Declaring tools: `tools.json`

The file sits at the pack root, next to `version.json`, and applies to
every agent:

```json
{
  "node": { "min_version": "20.0.0" },
  "openspec": {
    "min_version": "1.10.0",
    "install": { "manager": "npm", "package": "@fission-ai/openspec" }
  }
}
```

| Field | Meaning |
| --- | --- |
| key | The tool's name, used in notes and in `doctor`. |
| `command` | Executable looked up on PATH. Defaults to the key. Must be a plain name, not a path. |
| `min_version` | Required. Strict `MAJOR.MINOR.PATCH`. `v1.10`, ranges and prerelease tags are rejected. |
| `install.manager` | One of `npm`, `pnpm`, `bun`, `go`. |
| `install.package` | The package that provides the tool, without a version. |

A tool without `install` is only checked: here `node` is reported when
missing or older than 20.0.0, and the user installs it themselves. A pack
without `tools.json` requires nothing. Unknown fields are ignored, so a
pack can use fields from a newer wrapper without breaking older ones.

The pack names the package manager and the package. The command line comes
from the wrapper, so a pack can choose what gets installed but cannot make
the wrapper run arbitrary commands. `install` always pins `min_version`:

| `manager` | Command |
| --- | --- |
| `npm` | `npm install -g <package>@<min_version>` |
| `pnpm` | `pnpm add -g <package>@<min_version>` |
| `bun` | `bun add -g <package>@<min_version>` |
| `go` | `go install <package>@v<min_version>` |

Installing exactly `min_version` keeps every machine on the version the
pack was tested with. A user who already has a newer version passes the
check and is never downgraded.

## How a tool is checked

For each tool, in parallel:

1. Look up `command` on the PATH of the launch environment. Relative PATH
   entries (including an empty entry, which means the current directory)
   are skipped, so a launch never runs a file from the project the user
   is in. The running wrapper binary is skipped too. Not found is
   `missing`.
2. Run `<command> --version` with a 2 second timeout and take the first
   `MAJOR.MINOR.PATCH` in its output (stdout and stderr) that stands as
   its own word. `v22.22.0` reads as `22.22.0`; `built with go1.22.3`
   is not read as a version. A prerelease such as `1.10.0-rc.1` counts
   as lower than `1.10.0`. For a tool installed with `"manager": "go"` that
   prints no version, the module version that `go install` recorded in
   the binary is used instead, since many Go tools have no `--version`
   flag.
3. A version at or above `min_version` is `ok`. A lower version, no
   version at all, or a timeout is `too_old`.

`tools.Check` returns a `tools.Status` per tool with the state, the path
of the copy found first on PATH, and the version it reported.

## At launch

`wrapper.Prepare` loads `tools.json` from the pack and checks it. Each
problem becomes an entry in `Launch.Warnings`, and is also added to
`Launch.Notes`:

```
openspec 1.8.0 is older than the required 1.10.0. Run `wr init`.
node is not installed. Install node 20.0.0 or newer.
```

The `Run ...` suffix comes from `Options.SetupCommand`, so each wrapper
names its own command. A tool without `install`, or any tool when
`SetupCommand` is empty, gets the second form. A
`tools.json` that cannot be parsed becomes one warning
(`required tools were not checked: tools.json: ...`). Warnings never block
the launch. `wrapper.Run` prints them to `Options.Stderr` (standard error
by default) as `note: ...` lines before starting the agent.

`Options.SkipTools` turns the check off. `wr doctor` sets it and checks
the tools once itself, instead of once per agent launch it prepares.

## Installing: `wr init`

```
$ wr init
node: ok, 22.22.0 at /opt/node22/bin/node (minimum 20.0.0)
openspec: too old, 1.8.0 at /usr/local/bin/openspec (minimum 1.10.0)
will run:
  npm install -g @fission-ai/openspec@1.10.0
Run it? [y/N] y
$ npm install -g @fission-ai/openspec@1.10.0
... npm output ...
node: ok, 22.22.0 at /opt/node22/bin/node (minimum 20.0.0)
openspec: ok, 1.10.0 at /usr/local/bin/openspec (minimum 1.10.0)
```

Step by step:

1. Load `tools.json`. A malformed file is an error here.
2. Check every tool and print its state.
3. Plan one install per tool that is not `ok` (`tools.Plan`). Before
   planning, the wrapper asks the package manager where it puts
   executables (`npm prefix -g`, `$PNPM_HOME`, `$BUN_INSTALL/bin` or
   `~/.bun/bin`, `go env GOBIN GOPATH`) and checks that the directory is
   on PATH and writable. When a check fails, the tool gets a reason
   instead of a command, for example:

   ```
   x: cannot install: pnpm: PNPM_HOME is not set; run `pnpm setup` and open a new shell
   ```

   The same happens for a tool without `install`, for a manager this
   wrapper does not support, and when the old copy sits in a directory
   that comes before the install directory on PATH: the new copy would
   never be used, so init names both and installs nothing for that tool.
   A copy that did not report a version is not replaced either, since
   installing `min_version` over it could be a downgrade.
   Each package manager is asked once per plan.
4. Print the commands. `--dry-run` stops here. Without `--yes`, init asks
   for confirmation when standard input is a terminal; without a terminal
   it installs nothing and exits non-zero, so it never installs silently
   in CI.
5. Run each command with the user's environment and terminal
   (`tools.Run`). Nothing runs with sudo.
6. Check every tool again. When the first copy on PATH is still too old
   but a newer copy sits later on PATH, init prints both paths
   (`tools.Newer`).
7. Exit 0 only if every tool is `ok`.

Init never touches the current directory, any repository, or the agent's
configuration.

## Building the same command into another wrapper

`wr init` (`cmd/wr/init.go`) is built on four library calls. A wrapper
with a different name and its own prompt uses the same calls:

```go
reqs, err := tools.Load(packDir)            // nil when the pack has no tools.json
statuses := tools.Check(ctx, reqs, tools.CheckOptions{})
steps := tools.Plan(ctx, statuses, tools.PlanOptions{})
for _, step := range steps {
	if step.Err != nil {
		fmt.Printf("%s: cannot install: %v\n", step.Tool, step.Err)
		continue
	}
	// ask the user, then:
	err = tools.Run(ctx, step, nil, os.Stdin, os.Stdout, os.Stderr)
}
```

`tools.Managers()` lists the package managers this version supports.
