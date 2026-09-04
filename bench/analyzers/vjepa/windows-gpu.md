# Running the encoder on the Windows GPU box (RTX 4070)

The bench directory is not in git yet, so the box gets a copy of
`bench/` (clips, dataset.json, this analyzer) over SSH; the checkpoint is
downloaded there directly from Meta. Everything below is run from
PowerShell on the PC unless noted.

## One-time setup

1. OpenSSH server on Windows (Settings > Apps > Optional features >
   OpenSSH Server, then `Start-Service sshd; Set-Service sshd -StartupType Automatic`),
   and the Mac's public key appended to
   `C:\ProgramData\ssh\administrators_authorized_keys` (for an admin
   account) or `~\.ssh\authorized_keys` (for a normal one).
2. Python 3.12 and uv: `winget install astral-sh.uv` (uv can fetch 3.12 itself).
3. NVIDIA driver 550+ (CUDA 12.x runtime ships inside the torch wheel).
4. Repo checkout with the analyzer:
   ```
   cd $env:USERPROFILE\code
   git clone git@github.com:AdrienMrl/sentyx.git
   ```
   then from the Mac, copy the untracked bench data:
   ```
   rsync -av --exclude .venv --exclude features --exclude third_party \
     bench/ <pc>:code/sentyx/bench/
   ```
5. Environment:
   ```
   cd code\sentyx\bench\analyzers\vjepa
   uv venv --python 3.12 .venv
   uv pip install --python .venv\Scripts\python.exe torch torchvision --index-url https://download.pytorch.org/whl/cu124
   uv pip install --python .venv\Scripts\python.exe -r requirements.txt
   git clone --depth 1 https://github.com/facebookresearch/vjepa2 third_party\vjepa2
   .venv\Scripts\python.exe -m pytest -q tests
   ```
   Then apply the dtype patch to `third_party\vjepa2\app\vjepa_2_1\models\utils\modules.py`
   (copy the patched file from the Mac: it adds `q, k = q.to(v.dtype), k.to(v.dtype)`
   before both `scaled_dot_product_attention` calls).
6. Checkpoint (4.8 GB):
   ```
   mkdir $env:USERPROFILE\.cache\vjepa2
   curl.exe -L -o $env:USERPROFILE\.cache\vjepa2\vjepa2_1_vitl_dist_vitG_384.pt https://dl.fbaipublicfiles.com/vjepa2/vjepa2_1_vitl_dist_vitG_384.pt
   ```
   ViT-g (1B): `vjepa2_1_vitg_384.pt`.

## Encode + train

```
.venv\Scripts\python.exe vjepa_bench.py encode `
  -cache ..\..\features\vjepa21-vitl-384-3fps-pi-f16s4 `
  -encoder vjepa21 -model $env:USERPROFILE\.cache\vjepa2\vjepa2_1_vitl_dist_vitG_384.pt `
  -arch vjepa2_1_vit_large_384 -src third_party\vjepa2 -device cuda -dtype bfloat16 `
  -fps 3 -size 384 -crop full -frames 16 -stride 4 -match pi -flip
.venv\Scripts\python.exe vjepa_bench.py train -cache ..\..\features\vjepa21-vitl-384-3fps-pi-f16s4 `
  -head temporal -seeds 5 -flips -device cuda -out ..\..\features\vjepa21-vitl-384-3fps-pi-f16s4\train-temporal.json
```

Expected pace on a 4070: 0.1-0.2 s per window in bfloat16, the whole bench
with flips in a few minutes. Copy `train-*.json` (and a cache, if the Mac
should run `analyze` against it) back with rsync.
