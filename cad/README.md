# Tesyx CAD

This directory is the isolated, reproducible Python CAD environment for the
device enclosure and its assemblies. Source models will live here; the
authoritative geometry is Python, with STEP and STL exported for interchange
and fabrication.

## Current enclosure baseline

Use [design-basis.md](design-basis.md) for selected R1 components and mechanical
decisions, and [components.json](components.json) for explicitly provisional
packaging envelopes. These supersede the component assumptions in the older
`hardware/case/` draft. The implemented first pass is now in
[minimal_r1/](minimal_r1/README.md): source CAD, printable parts, STEP assemblies,
geometry checks and preview instructions. Its shell is 220 × 190 × 84mm; this
supersedes the initial packing estimate, while purchased-part fit remains unverified.

## Setup

```sh
cd cad
uv sync
```

Open this `cad` directory in VS Code, select `cad/.venv/bin/python` as the
Python interpreter, then install the **OCP CAD Viewer** extension
(`bernhard-42.ocp-cad-viewer`) and Microsoft Python extension. The viewer
receives objects sent by `ocp_vscode.show()`.

For a standalone local viewer, run:

```sh
cd cad
uv run python -m ocp_vscode
```

The viewer listens on `http://127.0.0.1:3939` by default. Keep generated STEP
and STL files under `cad/exports/`, which is intentionally ignored.
