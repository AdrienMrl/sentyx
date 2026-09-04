"""Encoders turn a window of frames into a token grid (T', H', W', D).

`VJEPA2Encoder` wraps the Hugging Face checkpoint; `FakeEncoder` produces
deterministic features from pixel statistics so the whole pipeline can be
exercised on a laptop with no weights and no GPU.
"""

import numpy as np


class EncoderInfo:
    def __init__(self, name, hidden, tubelet, patch, size):
        self.name, self.hidden, self.tubelet, self.patch, self.size = name, hidden, tubelet, patch, size

    def grid(self, frames):
        if frames % self.tubelet:
            raise ValueError(f"frames per window ({frames}) must be a multiple of tubelet size {self.tubelet}")
        if self.size % self.patch:
            raise ValueError(f"frame size {self.size} is not a multiple of patch {self.patch}")
        return frames // self.tubelet, self.size // self.patch, self.size // self.patch

    def as_dict(self):
        d = {"name": self.name, "hidden": self.hidden, "tubelet": self.tubelet,
             "patch": self.patch, "size": self.size}
        if getattr(self, "kind", None):
            d["kind"] = self.kind
        if getattr(self, "arch", None):
            d["arch"] = self.arch
        return d


class FakeEncoder:
    """Pixel-statistics stand-in: mean colour + gradient energy per tubelet,
    projected by a fixed random matrix. Cheap, deterministic, and informative
    enough that the heads can learn a synthetic signal in tests."""

    def __init__(self, size, hidden=64, tubelet=2, patch=16):
        self.info = EncoderInfo("fake", hidden, tubelet, patch, size)
        rng = np.random.default_rng(0)
        self.proj = rng.standard_normal((6, hidden)).astype(np.float32) / np.sqrt(6)

    def encode(self, window):
        """window: (T, S, S, 3) uint8 -> (T', H', W', D) float32."""
        t2, h2, w2 = self.info.grid(len(window))
        x = window.astype(np.float32) / 255.0
        p, tb = self.info.patch, self.info.tubelet
        x = x.reshape(t2, tb, h2, p, w2, p, 3)
        mean = x.mean(axis=(1, 3, 5))                                   # (t2,h2,w2,3)
        dy = np.abs(np.diff(x, axis=3)).mean(axis=(1, 3, 5))            # vertical edges
        dx = np.abs(np.diff(x, axis=5)).mean(axis=(1, 3, 5))            # horizontal edges
        dt = np.abs(np.diff(x, axis=1)).mean(axis=(1, 3, 5))            # motion inside tubelet
        feats = np.concatenate([mean, dy.mean(-1, keepdims=True), dx.mean(-1, keepdims=True),
                                dt.mean(-1, keepdims=True)], axis=-1)   # (t2,h2,w2,6)
        return feats @ self.proj


class VJEPA2Encoder:
    """Frozen V-JEPA 2 encoder from the Hugging Face hub.

    model: a hub id such as facebook/vjepa2-vitl-fpc64-256 (300M, 256 px),
           facebook/vjepa2-vith-fpc64-256, facebook/vjepa2-vitg-fpc64-256,
           facebook/vjepa2-vitg-fpc64-384 (1B, 384 px). V-JEPA 2.1 checkpoints
           should drop in unchanged once published under the same API.
    device: cuda, mps, or cpu.
    """

    def __init__(self, model, device, dtype):
        import torch
        from transformers import AutoModel, AutoVideoProcessor

        self.torch = torch
        self.device = device
        self.dtype = {"float32": torch.float32, "float16": torch.float16, "bfloat16": torch.bfloat16}[dtype]
        self.processor = AutoVideoProcessor.from_pretrained(model)
        self.model = AutoModel.from_pretrained(model, attn_implementation="sdpa", dtype=self.dtype).to(device).eval()
        cfg = self.model.config
        self.info = EncoderInfo(model, cfg.hidden_size, cfg.tubelet_size, cfg.patch_size, cfg.crop_size)

    def encode(self, window):
        t2, h2, w2 = self.info.grid(len(window))
        if window.shape[1] != self.info.size or window.shape[2] != self.info.size:
            raise ValueError(f"frames are {window.shape[1:3]}, encoder expects {self.info.size}x{self.info.size}; "
                             "sample with -size equal to the checkpoint's crop_size")
        # The processor accepts (T, H, W, C) uint8; with frames already at
        # crop_size its resize and crop are identities and only normalization runs.
        inputs = self.processor(window, return_tensors="pt")
        pv = inputs["pixel_values_videos"].to(self.device, self.dtype)
        with self.torch.no_grad():
            out = self.model(pixel_values_videos=pv, skip_predictor=True)
        tokens = out.last_hidden_state[0].float().cpu().numpy()   # (N, D)
        n = t2 * h2 * w2
        if tokens.shape[0] != n:
            raise RuntimeError(f"encoder returned {tokens.shape[0]} tokens, expected {n} = {t2}x{h2}x{w2}; "
                               "check tubelet/patch assumptions against the checkpoint config")
        # Token order is temporal-major then row-major spatial, matching the
        # patch embedding's flattening of (T', H', W').
        return tokens.reshape(t2, h2, w2, -1)


class VJEPA21Encoder:
    """Frozen V-JEPA 2.1 encoder through Meta's own code (facebookresearch/vjepa2,
    vendored under third_party/vjepa2) since no official transformers port
    exists yet. `model` is a local checkpoint such as
    ~/.cache/vjepa2/vjepa2_1_vitl_dist_vitG_384.pt, downloaded from
    https://dl.fbaipublicfiles.com/vjepa2/; `arch` names the hub entry
    (vjepa2_1_vit_base_384 / vit_large_384 / vit_giant_384 / vit_gigantic_384).

    Token order is the same as the HF V-JEPA 2 port: Conv3d -> flatten -> so
    (T', H', W') with time outermost."""

    MEAN = (0.485, 0.456, 0.406)
    STD = (0.229, 0.224, 0.225)
    KEYS = {"vjepa2_1_vit_base_384": "ema_encoder", "vjepa2_1_vit_large_384": "ema_encoder",
            "vjepa2_1_vit_giant_384": "target_encoder", "vjepa2_1_vit_gigantic_384": "target_encoder"}

    def __init__(self, model, arch, src, device, dtype):
        import os
        import sys

        import torch

        if arch not in self.KEYS:
            raise ValueError(f"unknown V-JEPA 2.1 arch {arch!r}; choose from {sorted(self.KEYS)}")
        if not os.path.isfile(model):
            raise FileNotFoundError(f"{model}: V-JEPA 2.1 checkpoint not found (download it from dl.fbaipublicfiles.com/vjepa2)")
        if not os.path.isfile(os.path.join(src, "hubconf.py")):
            raise FileNotFoundError(f"{src}: not a facebookresearch/vjepa2 checkout")
        sys.path.insert(0, src)
        from src.hub import backbones

        self.torch = torch
        self.device = device
        self.dtype = {"float32": torch.float32, "float16": torch.float16, "bfloat16": torch.bfloat16}[dtype]
        encoder, _predictor = getattr(backbones, arch)(pretrained=False)
        state = torch.load(model, map_location="cpu", weights_only=False)
        encoder.load_state_dict(backbones._clean_backbone_key(state[self.KEYS[arch]]), strict=True)
        del state, _predictor
        self.model = encoder.to(device=device, dtype=self.dtype).eval()
        self.info = EncoderInfo(os.path.basename(model), encoder.embed_dim,
                                encoder.tubelet_size, encoder.patch_size, 384)
        self.info.kind, self.info.arch = "vjepa21", arch
        self.mean = torch.tensor(self.MEAN, device=device).view(1, 3, 1, 1, 1)
        self.std = torch.tensor(self.STD, device=device).view(1, 3, 1, 1, 1)

    def encode(self, window):
        t2, h2, w2 = self.info.grid(len(window))
        if window.shape[1] != self.info.size or window.shape[2] != self.info.size:
            raise ValueError(f"frames are {window.shape[1:3]}, encoder expects {self.info.size}x{self.info.size}")
        torch = self.torch
        x = torch.from_numpy(window).to(self.device)                       # (T, S, S, 3) uint8
        x = x.permute(3, 0, 1, 2).unsqueeze(0).float() / 255.0             # (1, 3, T, S, S)
        x = ((x - self.mean) / self.std).to(self.dtype)
        with torch.no_grad():
            out = self.model(x)                                             # (1, N, D)
        tokens = out[0].float().cpu().numpy()
        if not np.isfinite(tokens).all():
            raise RuntimeError("non-finite encoder output; use -dtype float32 (fp16 attention can overflow)")
        assert tokens.shape[0] == t2 * h2 * w2, (tokens.shape, (t2, h2, w2))
        return tokens.reshape(t2, h2, w2, -1)


def build_encoder(kind, model, device, dtype, size, arch=None, src=None):
    if kind == "fake":
        return FakeEncoder(size)
    if kind == "vjepa2":
        if not model:
            raise ValueError("-model is required for the vjepa2 encoder")
        return VJEPA2Encoder(model, device, dtype)
    if kind == "vjepa21":
        if not (model and arch and src):
            raise ValueError("-model (checkpoint .pt), -arch and -src are required for the vjepa21 encoder")
        return VJEPA21Encoder(model, arch, src, device, dtype)
    raise ValueError(f"unknown encoder {kind!r}")
