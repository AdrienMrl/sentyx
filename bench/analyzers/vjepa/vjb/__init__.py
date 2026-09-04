"""V-JEPA 2 contact-detection experiment for teslcam-bench.

Pipeline: clip -> sampled frames -> frozen V-JEPA 2 encoder -> cached tokens
-> small heads (attentive probe, temporal head) trained by leave-one-case-out
cross-validation -> a verdict that honors the bench's -analyzer contract.
"""
