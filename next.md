# Next experiment: reduce false positives

Superseded objective (2026-09-10): the user now requires <5% false positives
and <50% misses. Active research protocol and recovery audit:
`bench/experiments/contact-goal/PROTOCOL.md`. Older results below remain
historical development evidence, not a final held-out test.

## Objective

Reach **less than 10% false positives on no-contact Sentry clips** before
considering the product acceptable. Initially aim to preserve at least the
current **~78% contact detection** (18/23), then improve both metrics. The
contact-detection target is proposed; the <10% false-positive requirement is
user-defined. Here, “negative” means no physical contact, not merely low threat.

## Current evidence

- Temporal: 18/23 target contacts detected, 3/11 negative clips falsely flagged.
- Candidate verifier: 18/23 detected, 2/11 negatives falsely flagged (~18%).
- These thresholds were explored on the same small real dataset. They do not
  establish an 18% false-positive rate on new footage.
- All 23 reviewed target-contact clips contained contact in at least one of the
  temporal head's three candidate excerpts, including its five missed contacts.
- At the verifier's matched-detection threshold, the remaining false alarms
  involve nearby people and a departing car.
- Perfect or near-perfect synthetic validation has not reliably transferred to
  real footage. More copies or visual polish alone is not a demonstrated fix.

## Proposed experiment

1. Expand synthetic **no-contact situations**, prioritizing:
   - People approaching, lingering near the car, and leaving.
   - Cars parking and departing.
   - Doors opening without touching the recording car.
   - Camera shake and lighting changes.

   Vary camera placement, actors, vehicles, backgrounds, motion, and lighting.
   Include comparable contact examples so the model must distinguish the
   interaction rather than identify the scene or actor. Keep related scenes
   and paired variants together when splitting training and validation data.

2. Retrain temporal and the candidate verifier with the added examples.
   Keep the existing training data and evaluation procedure as the baseline;
   make this a controlled dataset experiment before scaling generation further.
   Continue to keep real evaluation footage out of training.

3. Evaluate on **at least 100 new, representative no-contact Sentry clips**,
   plus a separate set of contact clips. Reserve this footage for evaluation
   and select the operating threshold beforehand using development data.
   Report false positives and contact detection together, including raw counts
   and uncertainty. Fewer than 10 false alarms out of 100 meets the observed
   sample target, but does not by itself establish that the underlying rate is
   below 10%; assess uncertainty before claiming the product target is met.

## Decision

Prioritize representative scenario variety over more copies or prettier
versions of the existing scenes. Accept a change only if it reduces false
alarms while retaining useful contact detection on the independent evaluation.

## Existing results

- `bench/reports/head-comparison/README.md`
- `bench/reports/head-comparison/comparison-corrected.json`
- `bench/reports/candidate-review/coverage.json`
- `bench/reports/candidate-verifier-v1/README.md`
- `bench/reports/candidate-verifier-v1/comparison.json`
