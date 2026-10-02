"""The small trainable parts on top of frozen encoder tokens.

AttentiveProbe   the V-JEPA papers' evaluation head: a learnable query
                 cross-attends over all tokens of a window, then a linear layer.
                 One logit per window; the clip logit is the max over windows.
TemporalHead     tokens pooled per timestep (spatial mean and max), a small
                 dilated 1-D convolution stack over time, one logit per
                 timestep. Gives the touch frame from the same pass.
MotionHead       TemporalHead plus the same pooling of the temporal difference
                 grid, so a motion discontinuity survives the spatial pooling
                 that a plain mean/max erases.
RegionHead       keeps coarse spatial structure: the token grid is pooled to
                 4x4 regions, each projected by a shared linear layer, and the
                 flattened regions feed the same temporal conv stack, so
                 "something changed at the car body" is separable from
                 "something changed in the frame".
MeanPoolLogistic the floor: logistic regression on the mean token of a window.

Every head splits its work into `prepare` (a reduction of the standardized
token grid that does not depend on trainable weights, so training computes it
once per fold and per clip) and `forward_rep` (the trainable part). `forward`
chains the two for inference. Inputs are (W, T', H', W', D) tensors for one
clip; outputs are (window_logits, timestep_logits), the latter None unless the
head localizes.
"""

import torch
import torch.nn as nn
import torch.nn.functional as F


class Head(nn.Module):
    """Base: standardizes tokens with per-dimension statistics of the training
    fold (set by `fit`, saved with the weights) so a few hundred steps of a
    small head are not spent rediscovering the encoder's scale."""

    def __init__(self, dim):
        super().__init__()
        self.register_buffer("mu", torch.zeros(dim))
        self.register_buffer("sigma", torch.ones(dim))

    def set_stats(self, mu, sigma):
        self.mu.copy_(mu)
        self.sigma.copy_(sigma.clamp_min(1e-6))

    def standardize(self, tokens):
        return (tokens.float() - self.mu) / self.sigma

    def forward(self, tokens):
        return self.forward_rep(self.prepare(self.standardize(tokens)))

    # Heads whose `prepare` commutes with per-dimension standardization
    # (means and maxima do; attention does not) implement `prepare_raw` and
    # `standardize_rep` so training reduces each clip's token grid once and
    # never holds the grids on the device.
    prepare_raw = None

    # --- to be provided by each head ---
    def prepare(self, tokens_std):
        raise NotImplementedError

    def augment(self, rep, rng, hp):
        raise NotImplementedError

    def forward_rep(self, rep):
        raise NotImplementedError


def _time_crop(x, rng, frac):
    """Random contiguous crop along axis 1 keeping at least two steps."""
    if frac <= 0:
        return x
    t = x.shape[1]
    keep = max(2, int(round(t * (1 - rng.uniform(0, frac)))))
    s = int(rng.integers(0, t - keep + 1))
    return x[:, s : s + keep]


def _noise(x, frac):
    if frac <= 0:
        return x
    return x + frac * x.std() * torch.randn_like(x)


class AttentiveProbe(Head):
    """Attention over every token of a window. To keep a step affordable on
    2048 tokens x 1024 dims, tokens are first projected onto the top
    `proj_dim` principal components of the training fold (a fixed, label-free
    basis fitted by `fit_projection` and saved with the head), so the trainable
    attention works in `proj_dim` dimensions."""

    def __init__(self, dim, heads=8, hidden=256, proj_dim=256):
        super().__init__(dim)
        proj_dim = min(proj_dim, dim)
        self.register_buffer("basis", torch.zeros(dim, proj_dim))
        self.norm = nn.LayerNorm(proj_dim)
        self.query = nn.Parameter(torch.zeros(1, 1, proj_dim))
        nn.init.trunc_normal_(self.query, std=0.02)
        self.attn = nn.MultiheadAttention(proj_dim, heads, batch_first=True)
        self.mlp = nn.Sequential(nn.LayerNorm(proj_dim), nn.Linear(proj_dim, hidden), nn.GELU(), nn.Linear(hidden, 1))

    @torch.no_grad()
    def fit_projection(self, sample_std):
        """sample_std: (n, D) standardized tokens from the training fold."""
        x = sample_std.float().cpu()
        cov = x.T @ x / max(1, x.shape[0] - 1)
        evals, evecs = torch.linalg.eigh(cov)                        # ascending
        k = self.basis.shape[1]
        self.basis.copy_(evecs[:, -k:].flip(-1).to(self.basis.device))

    def prepare(self, tokens_std):
        # Project, keep the grid in half precision: the probe needs every token
        # and the cache for a long clip is hundreds of MB.
        return (tokens_std @ self.basis).half()

    def augment(self, rep, rng, hp):
        rep = _time_crop(rep, rng, hp["time_crop"])
        if hp["token_drop"] > 0:
            mask = torch.rand(rep.shape[:-1], device=rep.device) >= hp["token_drop"]
            rep = rep * mask.unsqueeze(-1).to(rep.dtype)
        return rep

    def forward_rep(self, rep):
        w = rep.shape[0]
        x = self.norm(rep.float().reshape(w, -1, rep.shape[-1]))   # (W, N, D)
        q = self.query.expand(w, -1, -1)
        pooled, _ = self.attn(q, x, x, need_weights=False)          # (W, 1, D)
        return self.mlp(pooled[:, 0]).squeeze(-1), None              # (W,)


class ConvStackHead(Head):
    """Shared trunk of the localizing heads: a per-timestep feature vector of
    `in_dim` goes through a linear layer and a residual stack of dilated 1-D
    convolutions, and every timestep gets a logit. The window logit is the max
    over timesteps, so the same pass says both whether and when.

    Parameter names (`inp`, `convs`, `norms`, `out`) are shared by every
    subclass, and match what TemporalHead has always saved."""

    def __init__(self, dim, in_dim, width=128, layers=3):
        super().__init__(dim)
        self.inp = nn.Linear(in_dim, width)
        self.convs = nn.ModuleList(
            nn.Conv1d(width, width, kernel_size=3, padding=2 ** i, dilation=2 ** i) for i in range(layers))
        self.norms = nn.ModuleList(nn.GroupNorm(1, width) for _ in range(layers))
        self.out = nn.Conv1d(width, 1, kernel_size=1)

    def augment(self, rep, rng, hp):
        rep = _time_crop(rep, rng, hp["time_crop"])
        if hp["token_drop"] > 0:
            rep = F.dropout(rep, hp["token_drop"], training=True)
        return _noise(rep, hp["noise"])

    def temporal_logits(self, steps):
        """steps: (W, T', in_dim) -> (window_logits, timestep_logits)."""
        x = F.gelu(self.inp(steps)).transpose(1, 2)                  # (W, C, T')
        for conv, norm in zip(self.convs, self.norms):
            x = x + F.gelu(norm(conv(x)))
        step = self.out(x)[:, 0]                                     # (W, T')
        return step.amax(dim=1), step

    def forward_rep(self, rep):
        return self.temporal_logits(rep)


class TemporalHead(ConvStackHead):
    def __init__(self, dim, width=128, layers=3):
        super().__init__(dim, 2 * dim, width, layers)

    def prepare(self, tokens_std):
        w, t = tokens_std.shape[:2]
        flat = tokens_std.reshape(w, t, -1, tokens_std.shape[-1])   # (W, T', HW, D)
        return torch.cat([flat.mean(2), flat.amax(2)], dim=-1)      # (W, T', 2D)

    def prepare_raw(self, tokens):
        return self.prepare(tokens.float())

    def standardize_rep(self, raw):
        mu, sigma = torch.cat([self.mu, self.mu]), torch.cat([self.sigma, self.sigma])
        return (raw - mu) / sigma


class MotionHead(ConvStackHead):
    """TemporalHead's per-timestep summary, doubled with the same summary of
    the temporal difference of the token grid. Spatial mean and max over a 24x24
    grid wash out a hand-sized change; the difference grid is near zero
    everywhere the scene is still, so whatever moved survives the same pooling.

    The representation per timestep is (mean, max, diff-mean, diff-max), 4D
    wide. Both halves commute with per-dimension standardization: means and
    maxima are affine in the tokens, and the difference cancels the mean, which
    is why `standardize_rep` divides the difference half by sigma alone."""

    def __init__(self, dim, width=128, layers=3):
        super().__init__(dim, 4 * dim, width, layers)

    @staticmethod
    def _steps(grid):
        """grid: (W, T', H', W', D) float -> (W, T', 4D)."""
        w, t = grid.shape[:2]
        flat = grid.reshape(w, t, -1, grid.shape[-1])                # (W, T', HW, D)
        diff = torch.zeros_like(flat)
        diff[:, 1:] = flat[:, 1:] - flat[:, :-1]                     # t=0 has no predecessor
        return torch.cat([flat.mean(2), flat.amax(2), diff.mean(2), diff.amax(2)], dim=-1)

    def prepare(self, tokens_std):
        return self._steps(tokens_std)

    def prepare_raw(self, tokens):
        return self._steps(tokens.float())

    def standardize_rep(self, raw):
        zero = torch.zeros_like(self.mu)
        mu = torch.cat([self.mu, self.mu, zero, zero])
        sigma = self.sigma.repeat(4)
        return (raw - mu) / sigma


class RegionHead(ConvStackHead):
    """Coarse spatial structure kept instead of pooled away: the H'xW' token
    grid is averaged into a `regions`x`regions` map, so a change confined to
    one part of the frame stays in its own channel group. A shared linear layer
    squeezes each region token to `region_dim` before the regions are flattened
    for the temporal stack, which keeps the trainable size in the same range as
    TemporalHead rather than regions x D wide."""

    def __init__(self, dim, regions=4, region_dim=64, width=128, layers=3):
        super().__init__(dim, regions * regions * region_dim, width, layers)
        self.regions = regions
        self.proj = nn.Linear(dim, region_dim)

    def _pool(self, grid):
        """grid: (W, T', H', W', D) float -> (W, T', R*R, D)."""
        w, t, h, ww, d = grid.shape
        x = grid.permute(0, 1, 4, 2, 3).reshape(w * t, d, h, ww)
        x = F.adaptive_avg_pool2d(x, (self.regions, self.regions))   # (W*T', D, R, R)
        return x.reshape(w, t, d, self.regions * self.regions).permute(0, 1, 3, 2)

    def prepare(self, tokens_std):
        return self._pool(tokens_std)

    def prepare_raw(self, tokens):
        return self._pool(tokens.float())

    def standardize_rep(self, raw):
        # raw is (W, T', R*R, D); the statistics are per token dimension and
        # broadcast over the region axis.
        return (raw - self.mu) / self.sigma

    def forward_rep(self, rep):
        w, t = rep.shape[:2]
        steps = self.proj(rep).reshape(w, t, -1)                     # (W, T', R*R*region_dim)
        return self.temporal_logits(steps)


class LocalGridHead(Head):
    """Score local neighborhoods before spatial max pooling.

    The fixed 8x8 grid bounds CPU memory, retaining more spatial detail than
    RegionHead's 4x4 grid. This is not pixel-level contact localization.
    Shared projection and convolutions learn interactions between neighbors;
    timestep/window scores are maxima of the resulting local logit map.
    """

    def __init__(self, dim, regions=8, width=32):
        super().__init__(dim)
        self.regions = regions
        self.proj = nn.Linear(dim, width)
        self.local = nn.Sequential(nn.Conv3d(width, width, 3, padding=1), nn.GELU(),
                                   nn.Conv3d(width, width, 3, padding=1), nn.GELU())
        self.out = nn.Conv3d(width, 1, 1)

    def prepare(self, tokens_std):
        w, t, h, ww, d = tokens_std.shape
        x = tokens_std.permute(0, 1, 4, 2, 3).reshape(w * t, d, h, ww)
        x = F.adaptive_avg_pool2d(x.float(), (self.regions, self.regions))
        return x.reshape(w, t, d, self.regions, self.regions).permute(0, 1, 3, 4, 2)

    def prepare_raw(self, tokens):
        return self.prepare(tokens).half()

    def standardize_rep(self, raw):
        return ((raw.float() - self.mu) / self.sigma).half()

    def augment(self, rep, rng, hp):
        rep = _time_crop(rep, rng, hp["time_crop"])
        if hp["token_drop"] > 0:
            rep = F.dropout(rep, hp["token_drop"], training=True)
        return _noise(rep, hp["noise"])

    def local_logits(self, rep):
        x = F.gelu(self.proj(rep.float())).permute(0, 4, 1, 2, 3)
        return self.out(self.local(x))[:, 0]

    def forward_rep(self, rep):
        step = self.local_logits(rep).amax(dim=(-1, -2))
        return step.amax(dim=1), step


class MeanPoolLogistic(Head):
    def __init__(self, dim):
        super().__init__(dim)
        self.lin = nn.Linear(dim, 1)

    def prepare(self, tokens_std):
        w = tokens_std.shape[0]
        return tokens_std.reshape(w, -1, tokens_std.shape[-1]).mean(1)   # (W, D)

    def prepare_raw(self, tokens):
        return self.prepare(tokens.float())

    def standardize_rep(self, raw):
        return (raw - self.mu) / self.sigma

    def augment(self, rep, rng, hp):
        if hp["token_drop"] > 0:
            rep = F.dropout(rep, hp["token_drop"], training=True)
        return _noise(rep, hp["noise"])

    def forward_rep(self, rep):
        return self.lin(rep).squeeze(-1), None


HEADS = {"probe": AttentiveProbe, "temporal": TemporalHead, "motion": MotionHead,
         "region": RegionHead, "local": LocalGridHead, "meanpool": MeanPoolLogistic}


def build_head(kind, dim):
    if kind not in HEADS:
        raise ValueError(f"unknown head {kind!r}; choose from {sorted(HEADS)}")
    return HEADS[kind](dim)
