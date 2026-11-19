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
| `cmd/python-shrinks` | No new Python. Four totals never rise above `scripts/baselines/python.json` (code owner review). No file list, no exception list: a rename passes. What each total counts is below. |
| `cmd/no-hand-marks` | The platform's own automation leaves no mark that check FND-G02 (Zero touch) reads as a hand. In the start-up data of the nodes (`infra/providers/`) and in every chart: no `kubectl rollout restart` (a restart is a patch of `catalyst.openova.io/restartedAt` and `catalyst.openova.io/restartedBy`), and no kubectl apply, patch, annotate, label, scale or set of a HelmRelease, or of a Deployment, DaemonSet or StatefulSet that can be in catalyst-system, cnpg-system, kube-system or flux-system, without `--field-manager=catalyst-<component>`. `--list` also prints every kubectl write it read. What it reads and cannot see is in the comment of `main.go`. |

`go run ./cmd/python-shrinks --list` prints the share of every file of each total and every place it counts;
`--write-baseline` writes the totals of the tree and refuses to raise any (a total that fell passes and says the baseline can be lowered);
`--base <commit>` lists the files that grew against that commit and uses the lower of the baseline file, the file at that commit and, when the file exists there, the total of that commit itself (room freed by one change is not taken by the next). When the file at that commit holds another schema, the change under test changes the schema: that commit has nothing to compare and the baseline file alone is used; a file of this schema that cannot be read there stops the guard (exit 2).

### `python-shrinks`: what each total counts

It reads the files Git lists (tracked, and new files Git does not ignore).

- `python_files`: every file whose path ends in `.py`, and every other file whose first line is a `#!` line that names python.
- `python_file_lines`: the lines of those files.
- `embedded_lines`: the lines of Python inside every other file of code or configuration: shell, YAML, JSON, templates (`.tpl` `.tftpl` `.j2`), Terraform and HCL, make and just files, Go, Rust, TypeScript, JavaScript, a chart template or file, and any file whose first line is a `#!` line; never a `.md` or `.txt` file. Counted:
  - the body of a here-document opened on a line that runs python or writes a `.py` file;
  - the body of any other here-document whose first line is a python `#!` line, that line included (`cat > "$BIN/curl" <<'PY'` and then `#!/usr/bin/env python3`: a program saved without the `.py` suffix and run later);
  - the program of `python -c` and of a here-string (`<<<`) given to python, from quote to quote; when it is `"$NAME"`, the text or here-document assigned to `NAME`;
  - a YAML block (`|` or `>`) whose key ends in `.py`, or whose first line is a python `#!` line;
  - the run of a workflow or action step whose shell is python (its own `shell:` or the `defaults: run: shell:` of its job or workflow);
  - a Go, Rust, TypeScript or JavaScript string that starts with a python `#!` line (a stub that a test writes with `os.WriteFile` and runs): its lines from the `#!` line to the last line with text.

  A shell variable that the file itself assigns a name of python (`PYTHON=python3`, `PYTHON="${PYTHON:-python3}"`, `PY=$(command -v python3)`) is python in that file: `"$PYTHON" -c "..."`, `"$PYTHON" - <<'PY'` and `"${PY_BIN}" - <<'PY'` count as they do with `python3`, and a line that names the variable counts as a place.

  Any other string of a Go, Rust, TypeScript or JavaScript file is data (the fixture of a test). Of such a file only these lines are read for the rest: a `//go:generate` line, a line with a string that is only the name of python (`"python3"`), and a call that starts a process (`exec.Command`, `Command::new`, `spawn`, `execSync`, ...) from its name to its closing parenthesis.
- `sovereign_places`: in what reaches a Sovereign or a shipped image (chart templates and files, the cloud-init templates and the Terraform under `infra/providers/`, container files): a package install that names a python package, a `pip install`, an image whose name starts with python (`FROM`, `image:`), a command that names python, and a python `#!` line.

### Known limits

The guard reads text as it stands in the tree. By design it does not see:

- Python assembled or fetched at run time: a program built from pieces (`"#!" + rest`, a `printf` of fragments), downloaded, or read from a Secret, a ConfigMap or an environment variable; also a program that Go, Rust or TypeScript holds in a variable and passes to `python -c`.
- A Python program saved without the `.py` suffix and without a python `#!` line. Only programs and files are counted, never the call that runs them, so such a program is not seen where it is called either.
- An encoded payload: base64, compressed or otherwise encoded text that becomes Python when decoded.
- A file run by an interpreter chosen at run time: `"$INTERP" "$f"`, `env "$X"`, a name looked up in `PATH`. A variable that the same file assigns a name of python is not one of these: it is read as python.

`docs/PRINCIPLES.md` IV.4 forbids all of these the same as the shapes the guard counts: no new Python is written, in any form. Review holds what the guard cannot see.

A call of a Python program (`python3 scripts/x.py`, `python3 scripts/plan.py accept`, `exec.Command("python3", script)`, `//go:generate python3 gen.py`) is not one of these. It writes no Python: it is allowed, and the guard does not count it.
