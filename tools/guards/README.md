# tools/guards — repository tooling in Go

The home of the guards, checks and tools that read this repository's tree. New repository
tooling is written here, in Go, never as a new Python file (docs/PRINCIPLES.md IV.4).

- One Go module, standard library only. A command lives in `cmd/<name>/`, its tests beside it
  (`go test` is the self-test: every rule is shown to fail on a tree built by the test).
- A command is run from this folder and finds the repository root with Git:

  ```sh
  cd tools/guards && go test -count=1 ./...
  cd tools/guards && go run ./cmd/<name> [flags]
  ```

- A guard is wired as steps of `.github/workflows/tree-guards.yaml`: one step that runs the
  tests, one that runs the command. The Go of the job is set up from this `go.mod`.
- A baseline of a guard lives in `scripts/baselines/<name>.json` and only falls.

## Commands

| Command | What it holds |
|---|---|
| `cmd/python-shrinks` | No new Python file, no more lines of Python, no new place where Python runs on a Sovereign or is put into a shipped image. Baseline `scripts/baselines/python.json`. |

`go run ./cmd/python-shrinks --list` prints every Python file and every place it counts;
`--write-baseline` lowers the baseline after Python was removed and refuses to raise it;
`--base <commit>` also fails when the baseline file is higher than at that commit.
