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
| `cmd/python-shrinks` | No new Python. Five totals never rise above `scripts/baselines/python.json` (code owner review): `python_files` (`.py` files and python `#!` scripts), `python_file_lines`, `embedded_lines` (Python inside shell, workflow, chart, plan, Terraform and template files: a here-document or `-c` program given to python, a `.py` key of a ConfigMap, the run of a `shell: python` step), `invoke_lines` (lines of those files that run python as a command, such as `python3 <path>`), `sovereign_places` (where Python runs on a Sovereign or is installed in a shipped image: chart templates and files, cloud-init templates and the Terraform under `infra/providers/`, container files). No file list, no exception list: a rename passes. |

`go run ./cmd/python-shrinks --list` prints the share of every file of each total and every place it counts;
`--write-baseline` writes the totals of the tree and refuses to raise any (a total that fell passes and says the baseline can be lowered);
`--base <commit>` lists the files that grew against that commit and uses the lower of the baseline file and the file at that commit.
