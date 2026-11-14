# Plan tracker

This file held a status board that a scheduled job outside the repository rewrote. The board is replaced by the lines below.
`python3 scripts/plan.py render` writes the state of every work package and the gate verdict into this file.
The measure of done is the gate verdict: `python3 scripts/acceptance-gate.py verdict`.
