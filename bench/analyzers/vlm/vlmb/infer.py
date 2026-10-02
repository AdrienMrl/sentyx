"""Scoring one clip with Qwen3.8.

The model is asked a one-word question and the answer is never sampled;
instead the logits for the first answer token are captured through a
`logits_processors` hook and reduced to P(yes) normalised over just the yes
and no token families. Sampling a single word would throw away the model's
confidence, and confidence is the whole point: with phantom alerts as the
expensive error, the decision threshold has to be movable after the fact.
"""

import math

import mlx.core as mx
import numpy as np
from mlx_vlm import apply_chat_template, generate, load
from mlx_vlm.utils import load_config

from . import prompt as P


def _token_ids(tokenizer, forms):
    """First-token ids for each surface form, de-duplicated."""
    ids = set()
    for form in forms:
        enc = tokenizer.encode(form, add_special_tokens=False)
        if enc:
            ids.add(int(enc[0]))
    return sorted(ids)


class ContactScorer:
    def __init__(self, model_path, adapter_path=None):
        self.model, self.processor = (
            load(model_path, adapter_path=adapter_path)
            if adapter_path
            else load(model_path)
        )
        self.config = load_config(model_path)
        tok = getattr(self.processor, "tokenizer", self.processor)
        self.yes_ids = _token_ids(tok, P.YES_FORMS)
        self.no_ids = _token_ids(tok, P.NO_FORMS)
        if not self.yes_ids or not self.no_ids:
            raise RuntimeError("could not resolve yes/no token ids")

    def _first_logits(self, frames):
        formatted = apply_chat_template(
            self.processor, self.config, P.PROMPT, num_images=len(frames)
        )
        captured = []

        def capture(tokens, logits):
            # mlx hands these back in the model's compute dtype, and numpy
            # cannot consume bfloat16 ("not a valid PEP 3118 buffer format"),
            # so the cast to float32 happens on the mlx side first.
            if not captured:
                captured.append(np.array(logits.astype(mx.float32), copy=True))
            return logits

        generate(
            self.model,
            self.processor,
            formatted,
            image=list(frames),
            max_tokens=1,
            temperature=0.0,
            logits_processors=[capture],
            verbose=False,
        )
        if not captured:
            raise RuntimeError("no logits captured; generate produced no step")
        logits = captured[0]
        return logits.reshape(-1) if logits.ndim > 1 else logits

    def p_contact(self, frames) -> float:
        """P(yes) over the yes/no token families only.

        Restricting the softmax to the two answer families keeps the score
        from being diluted by whatever else the model might have said, so a
        clip where it is torn between yes and no lands near 0.5 instead of
        near zero.
        """
        logits = self._first_logits(frames)
        top_yes = max(float(logits[i]) for i in self.yes_ids)
        top_no = max(float(logits[i]) for i in self.no_ids)
        m = max(top_yes, top_no)
        ey, en = math.exp(top_yes - m), math.exp(top_no - m)
        return ey / (ey + en)
