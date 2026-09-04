"""The small trainable parts on top of frozen encoder tokens.

AttentiveProbe   the V-JEPA papers' evaluation head: a learnable query
                 cross-attends over all tokens of a window, then a linear layer.
                 One logit per window; the clip logit is the max over windows.
TemporalHead     tokens pooled per timestep (spatial mean and max), a small
                 dilated 1-D convolution stack over time, one logit per
                 timestep. Gives the touch frame from the same pass.
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


class TemporalHead(Head):
    def __init__(self, dim, width=128, layers=3):
        super().__init__(dim)
        self.inp = nn.Linear(2 * dim, width)
        self.convs = nn.ModuleList(
            nn.Conv1d(width, width, kernel_size=3, padding=2 ** i, dilation=2 ** i) for i in range(layers))
        self.norms = nn.ModuleList(nn.GroupNorm(1, width) for _ in range(layers))
        self.out = nn.Conv1d(width, 1, kernel_size=1)

    def prepare(self, tokens_std):
        w, t = tokens_std.shape[:2]
        flat = tokens_std.reshape(w, t, -1, tokens_std.shape[-1])   # (W, T', HW, D)
        return torch.cat([flat.mean(2), flat.amax(2)], dim=-1)      # (W, T', 2D)

    def augment(self, rep, rng, hp):
        rep = _time_crop(rep, rng, hp["time_crop"])
        if hp["token_drop"] > 0:
            rep = F.dropout(rep, hp["token_drop"], training=True)
        return _noise(rep, hp["noise"])

    def forward_rep(self, rep):
        x = F.gelu(self.inp(rep)).transpose(1, 2)                    # (W, C, T')
        for conv, norm in zip(self.convs, self.norms):
            x = x + F.gelu(norm(conv(x)))
        step = self.out(x)[:, 0]                                     # (W, T')
        return step.amax(dim=1), step


class MeanPoolLogistic(Head):
    def __init__(self, dim):
        super().__init__(dim)
        self.lin = nn.Linear(dim, 1)

    def prepare(self, tokens_std):
        w = tokens_std.shape[0]
        return tokens_std.reshape(w, -1, tokens_std.shape[-1]).mean(1)   # (W, D)

    def augment(self, rep, rng, hp):
        if hp["token_drop"] > 0:
            rep = F.dropout(rep, hp["token_drop"], training=True)
        return _noise(rep, hp["noise"])

    def forward_rep(self, rep):
        return self.lin(rep).squeeze(-1), None


HEADS = {"probe": AttentiveProbe, "temporal": TemporalHead, "meanpool": MeanPoolLogistic}


def build_head(kind, dim):
    if kind not in HEADS:
        raise ValueError(f"unknown head {kind!r}; choose from {sorted(HEADS)}")
    return HEADS[kind](dim)
