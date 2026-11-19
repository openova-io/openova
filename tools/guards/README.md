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
| `cmd/python-shrinks` | Python only shrinks, file by file: no new file that holds Python (a `.py` file, a python `#!` script, or Python inside a shell, workflow, chart, plan or template file: a here-document or `-c` program given to python, a `.py` key of a ConfigMap), no file with more lines of Python than its baseline, no new place where Python runs on a Sovereign or is put into a shipped image. Baseline `scripts/baselines/python.json` (code owner review). |

`go run ./cmd/python-shrinks --list` prints every file that holds Python (`file` or `inside`) and every place it counts;
`--write-baseline` lowers the baseline after Python was removed (the guard fails until it is lowered) and refuses to raise it;
`--base <commit>` also fails when the baseline file is higher than at that commit.
