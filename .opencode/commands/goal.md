---
description: Set the research objective and arm the autonomous goal loop
---
Set the research objective to:

$ARGUMENTS

Then:
1. If the objective above is non-empty, run `teslcam-research goal -objective "<the objective>" -status active`. If it is empty, just run `teslcam-research goal -status active`.
2. Arm the loop with `touch bench/research/loop.enabled`.
3. Confirm the objective in one line, then start working on it. Keep going — the goal plugin re-prompts this session whenever it goes idle, so do not stop to wait for the user. Log every experiment, result, and decision with `teslcam-research add`, and route anything visual through gpt-6-astra.
