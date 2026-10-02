"""The question put to the model, and the training target.

The bench's primary label is `contact` — did anything physically touch the
car — so the model is asked exactly that and nothing else. The answer is
constrained to one word so the score can come from the yes/no logit rather
than from parsing prose: that turns a bare boolean into a continuous
probability, which is what lets a threshold be tuned for precision.

The framing is folded into the user turn rather than sent as a system message
because `apply_chat_template` builds the image placeholders itself and takes a
single prompt string.
"""

PROMPT = (
    "These frames are a chronological sequence from a parked car's own "
    "security camera, sampled evenly across one recorded event.\n\n"
    "Did anything physically touch this car during the clip? Count a person, "
    "a hand, a bag, a shopping cart, an animal, or another vehicle's door or "
    "body making contact with it. Someone walking past, looking at the car, "
    "or standing near it without touching it does not count.\n\n"
    "Answer with exactly one word: yes or no."
)

YES = "yes"
NO = "no"

# The same word can tokenize differently with a leading space or capital, and
# which one the template lands on is a property of the chat template rather
# than of the answer, so every surface form votes into its own bucket.
YES_FORMS = ["yes", "Yes", " yes", " Yes", "YES"]
NO_FORMS = ["no", "No", " no", " No", "NO"]


def target(contact: bool) -> str:
    return YES if contact else NO


def training_messages(n_frames: int, contact: bool):
    """One LoRA training row: the same prompt seen at inference, answered."""
    return [
        {
            "role": "user",
            "content": [{"type": "image"} for _ in range(n_frames)]
            + [{"type": "text", "text": PROMPT}],
        },
        {
            "role": "assistant",
            "content": [{"type": "text", "text": target(contact)}],
        },
    ]
