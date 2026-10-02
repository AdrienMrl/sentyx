"""Rank three separated temporal-head moments per target-contact clip.

No contact timestamps or YES/NO thresholds are used for proposal selection.
"""
import argparse
import json
from pathlib import Path


def select_moments(points, count=3, separation=3.0):
    chosen=[]
    for point in sorted(points,key=lambda p:(-p['logit'],p['seconds'])):
        if all(abs(point['seconds']-p['seconds'])>=separation for p in chosen):
            chosen.append(point)
        if len(chosen)==count:break
    return chosen


def main():
    import torch
    from vjb import store, train as tr
    p=argparse.ArgumentParser()
    p.add_argument('--dataset',required=True)
    p.add_argument('--cache',required=True)
    p.add_argument('--head',required=True)
    p.add_argument('--out',required=True)
    a=p.parse_args()
    head,ck=tr.load_head(a.head,'cuda')
    meta=store.read_meta(a.cache)
    assert meta==ck['meta']
    result={'version':1,'head':'temporal','excerpt_seconds':3.0,'selection':'Top logits, at least 3 seconds apart; no decision threshold or ground-truth times','cases':[]}
    with torch.inference_mode():
        for c in store.dataset_cases(a.dataset):
            if not c['contact'] or not any(t in c['tags'] for t in ('review:event:door_ding','review:event:vehicle_other')):continue
            feats=store.ClipFeatures(store.feature_path(a.cache,c['id'],c['clip']))
            _,steps=head(torch.from_numpy(feats.tokens).to('cuda'))
            logits=steps.float().cpu().numpy()
            points=[{'seconds':float(feats.timestep_seconds(w,t,meta['fps'],meta['encoder']['tubelet'])), 'logit':float(logits[w,t])} for w in range(logits.shape[0]) for t in range(logits.shape[1])]
            result['cases'].append({'id':c['id'],'source_clip':Path(c['clip']).name,'moments':select_moments(points)})
            print(c['id'],flush=True)
    Path(a.out).write_text(json.dumps(result,indent=2)+'\n')

if __name__=='__main__':main()
