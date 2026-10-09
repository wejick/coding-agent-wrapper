package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/wejick/coding-agent-wrapper/pack"
	"github.com/wejick/coding-agent-wrapper/tools"
)

// cmdInit installs the tools the pack requires. It checks each tool,
// prints the commands it would run, and runs them after the user agrees
// (or with --yes). Without a terminal and without --yes it installs
// nothing. It exits 0 only when every required tool is ok at the end.
func cmdInit(ctx context.Context) error {
	src, err := source()
	if err != nil {
		return err
	}
	var res *pack.FetchResult
	if g.refresh {
		res, err = refresh(ctx, src)
	} else {
		res, err = src.Fetch(ctx, pack.FetchOptions{})
	}
	if err != nil {
		return err
	}
	reqs, err := tools.Load(res.Dir)
	if err != nil {
		return err
	}
	if len(reqs) == 0 {
		fmt.Println("the pack requires no tools")
		return nil
	}

	statuses := tools.Check(ctx, reqs, tools.CheckOptions{})
	printStatuses(ctx, statuses)
	steps := tools.Plan(ctx, statuses, tools.PlanOptions{})
	if len(steps) == 0 {
		return nil
	}
	var runnable []tools.Step
	for _, st := range steps {
		if st.Err != nil {
			fmt.Printf("%s: cannot install: %v\n", st.Tool, st.Err)
			continue
		}
		runnable = append(runnable, st)
	}
	if len(runnable) == 0 {
		return fmt.Errorf("%s not ok and nothing can be installed", countTools(len(steps)))
	}
	fmt.Println("will run:")
	for _, st := range runnable {
		fmt.Println("  " + st.String())
	}
	if g.dryRun {
		return fmt.Errorf("dry run, nothing installed: %s not ok", countTools(len(steps)))
	}
	if !g.yes {
		if !isTerminal(os.Stdin) {
			return errors.New("not installing without a terminal; rerun with --yes to install")
		}
		fmt.Print("Run it? [y/N] ")
		if answer := strings.ToLower(strings.TrimSpace(readLine(os.Stdin))); answer != "y" && answer != "yes" {
			return errors.New("nothing installed")
		}
	}

	for _, st := range runnable {
		fmt.Println("$ " + st.String())
		if err := tools.Run(ctx, st, nil, os.Stdin, os.Stdout, os.Stderr); err != nil {
			fmt.Printf("%s: install failed: %v\n", st.Tool, err)
		}
	}

	after := tools.Check(ctx, reqs, tools.CheckOptions{})
	printStatuses(ctx, after)
	bad := 0
	for _, s := range after {
		if s.State != tools.OK {
			bad++
		}
	}
	if bad > 0 {
		return fmt.Errorf("%s not ok", countTools(bad))
	}
	return nil
}

// printStatuses prints one line per tool, and for a tool whose first copy
// on PATH is too old while a newer copy comes later, where both are.
func printStatuses(ctx context.Context, statuses []tools.Status) {
	for _, s := range statuses {
		fmt.Println(s.Describe())
		if newer, ok := tools.Newer(ctx, s, tools.CheckOptions{}); ok {
			fmt.Printf("  %s at %s comes first on PATH, before %s at %s; remove the older copy or move %s earlier on PATH\n",
				displayVersion(s), s.Path, newer.Version, newer.Path, filepath.Dir(newer.Path))
		}
	}
}

// readLine reads one line byte by byte, so input typed after the answer
// stays in stdin for the package managers that run next.
func readLine(f *os.File) string {
	var line []byte
	b := make([]byte, 1)
	for {
		n, err := f.Read(b)
		if n == 1 && b[0] == '\n' {
			break
		}
		line = append(line, b[:n]...)
		if err != nil {
			break
		}
	}
	return string(line)
}

func displayVersion(s tools.Status) string {
	if s.Version == "" {
		return "the copy with no version"
	}
	return s.Version
}

func countTools(n int) string {
	if n == 1 {
		return "1 required tool is"
	}
	return fmt.Sprintf("%d required tools are", n)
}
