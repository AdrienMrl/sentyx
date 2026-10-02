"""Report verifier ROC using raw logits to avoid saturated sigmoid ties."""
import json
from pathlib import Path
from compare_reviewed_heads import operating_points,auc,at_threshold
root=Path('bench/reports/candidate-verifier-v1')
x=json.load(open(root/'evaluate-real-verifier.json'));val=json.load(open(root/'evaluate-val-verifier.json'))
p=[dict(r,score=r['logit']) for r in x['cases'] if r['label'] and any(t in r['tags'] for t in ['review:event:door_ding','review:event:vehicle_other'])]
n=[dict(r,score=r['logit']) for r in x['cases'] if not r['label']]
pts=operating_points(p,n)
svpts=operating_points([dict(r,score=r['logit']) for r in val['cases'] if r['label']],[dict(r,score=r['logit']) for r in val['cases'] if not r['label']])
sv=max((r for r in svpts if r['false_alarms']<=1),key=lambda r:(r['detected'],-r['false_alarms']))
s=dict(threshold_units='raw logit, zero corresponds to sigmoid 0.5',auc=auc(p,n),positives=len(p),negatives=len(n),default=at_threshold(p,n,0),matched18=min((r for r in pts if r['detected']>=18),key=lambda r:(r['false_alarms'],r['detected'])),one_false_alarm=max((r for r in pts if r['false_alarms']<=1),key=lambda r:(r['detected'],-r['false_alarms'])),zero_false_alarms=max((r for r in pts if r['false_alarms']==0),key=lambda r:r['detected']),synthetic_threshold=dict(validation=sv,real=at_threshold(p,n,sv['threshold'])),roc=pts)
print(json.dumps({k:v for k,v in s.items() if k!='roc'},indent=2))
(root/'comparison.json').write_text(json.dumps(s,indent=2)+'\n')
