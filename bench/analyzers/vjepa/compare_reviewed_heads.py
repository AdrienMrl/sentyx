#!/usr/bin/env python3
"""Compare saved head scores on reviewed real clips at matched recall.

Thresholds selected on real clips describe an exploratory ROC curve, not an
independent deployment estimate. A separate synthetic-validation threshold
shows performance without selecting the threshold on the real examples.
"""
import argparse
import json
from pathlib import Path


def operating_points(positive, negative):
    # Predictions use >= threshold, matching the benchmark.
    thresholds = [float('inf')] + sorted({r['score'] for r in positive + negative}, reverse=True)
    return [dict(threshold=t if t != float('inf') else None,
                 detected=sum(r['score'] >= t for r in positive),
                 false_alarms=sum(r['score'] >= t for r in negative)) for t in thresholds]


def at_threshold(positive, negative, threshold):
    t = float('inf') if threshold is None else threshold
    return dict(threshold=threshold, detected=sum(r['score'] >= t for r in positive),
                false_alarms=sum(r['score'] >= t for r in negative))


def auc(positive, negative):
    return sum((p['score'] > n['score']) + .5 * (p['score'] == n['score'])
               for p in positive for n in negative) / (len(positive) * len(negative))


def compare(dataset, directory):
    labels = {c['id']: c for c in dataset['cases'] if c.get('label')}
    result = {}
    for name in ('temporal', 'motion', 'region'):
        real = json.loads((directory / f'evaluate-real45-{name}-comparison.json').read_text())
        synth = json.loads((directory / f'evaluate-synthetic-val-{name}-comparison.json').read_text())
        positive, negative = [], []
        for row in real['cases']:
            c = labels[row['case']]
            if not c['label']['contact']:
                negative.append(row)
            elif any(t in c.get('tags', []) for t in ('review:event:door_ding', 'review:event:vehicle_other')):
                positive.append(row)
        points = operating_points(positive, negative)
        synpos = [r for r in synth['cases'] if r['label']]
        synneg = [r for r in synth['cases'] if not r['label']]
        synpoint = max((p for p in operating_points(synpos, synneg) if p['false_alarms'] <= 1),
                       key=lambda p: (p['detected'], -p['false_alarms']))
        result[name] = dict(
            positives=len(positive), negatives=len(negative), auc=auc(positive, negative),
            default=at_threshold(positive, negative, .5),
            matched_18=min((p for p in points if p['detected'] >= 18), key=lambda p: (p['false_alarms'], p['detected'])),
            recall_90=min((p for p in points if p['detected'] >= .9 * len(positive)), key=lambda p: (p['false_alarms'], p['detected'])),
            zero_false_alarms=max((p for p in points if p['false_alarms'] == 0), key=lambda p: p['detected']),
            one_false_alarm=max((p for p in points if p['false_alarms'] <= 1), key=lambda p: (p['detected'], -p['false_alarms'])),
            synthetic_threshold=dict(validation=synpoint, real=at_threshold(positive, negative, synpoint['threshold'])),
            roc=points,
        )
    return result


if __name__ == '__main__':
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--dataset', type=Path, required=True)
    p.add_argument('--scores', type=Path, required=True)
    p.add_argument('--out', type=Path, required=True)
    a = p.parse_args()
    result = compare(json.loads(a.dataset.read_text()), a.scores)
    a.out.write_text(json.dumps(result, indent=2) + '\n')
    for name, row in result.items():
        print(name, json.dumps({k:v for k,v in row.items() if k!='roc'}))
