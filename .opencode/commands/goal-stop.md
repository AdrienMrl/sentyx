---
description: Stop the autonomous goal loop
---
Stop the autonomous goal loop:
1. Run `teslcam-research goal -status paused`.
2. Run `rm -f bench/research/loop.enabled`.
3. Confirm the objective and that the loop is off, then stop working until the user asks.
