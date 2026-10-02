"""Synthetic-only candidate verifier using cached three-second region sequences."""
import argparse
import json
from pathlib import Path
import numpy as np
import torch
from torch import nn
from torch.nn import functional as F
from vjb import store, train as tr
from vjb.heads import RegionHead
from vjb.progress import Writer
from propose_contact_moments import select_moments


def excerpt_bounds(center, duration):
    start=max(0.,min(center-1.5,max(0.,duration-3.)))
    return start,min(duration,start+3.)


def overlaps_contact(case,start,end):
    if not case['contact']:return False
    if case['start'] is None or case['end'] is None:
        raise ValueError('Synthetic contacts need generated event intervals')
    return start <= case['end'] and end >= case['start']


class Verifier(nn.Module):
    def __init__(self,dim=1024):
        super().__init__()
        self.project=nn.Linear(dim,32)
        self.conv=nn.Sequential(nn.Conv1d(512,128,3,padding=1),nn.GELU(),nn.Dropout(.1),nn.Conv1d(128,128,3,padding=2,dilation=2),nn.GELU())
        self.out=nn.Linear(256,1)
    def forward(self,x,mask):
        z=self.project(x).flatten(2).transpose(1,2)
        z=self.conv(z)
        mean=(z*mask[:,None,:]).sum(-1)/mask.sum(-1,keepdim=True).clamp_min(1)
        peak=z.masked_fill(~mask[:,None,:],-1e4).amax(-1)
        return self.out(torch.cat([mean,peak],-1)).squeeze(-1)


@torch.inference_mode()
def prepare(cases,cache,proposer,meta,progress):
    pool=RegionHead(meta['encoder']['hidden']).cuda()
    features,masks,labels,groups,info=[],[],[],[],[]
    for i,c in enumerate(cases):
        feat=store.ClipFeatures(store.feature_path(str(cache),c['id'],c['clip']))
        grid=torch.from_numpy(feat.tokens).cuda()
        _,step=proposer(grid)
        logits=step.float().cpu().numpy()
        times=np.array([[feat.timestep_seconds(w,t,meta['fps'],meta['encoder']['tubelet']) for t in range(logits.shape[1])] for w in range(logits.shape[0])])
        points=[dict(seconds=float(times[w,t]),logit=float(logits[w,t])) for w in range(logits.shape[0]) for t in range(logits.shape[1]) if times[w,t]<feat.n_frames/meta['fps']]
        moments=select_moments(points)
        raw=pool.prepare_raw(grid).flatten(0,1)
        flat_times=times.ravel()
        for m in moments:
            start,end=excerpt_bounds(m['seconds'],feat.n_frames/meta['fps'])
            ix=np.flatnonzero((flat_times>=start)&(flat_times<end))[:24]
            if not len(ix):raise ValueError('empty candidate')
            arr=torch.zeros(24,16,meta['encoder']['hidden'],dtype=torch.float16)
            arr[:len(ix)]=raw[ix].cpu().half()
            mask=torch.arange(24)<len(ix)
            features.append(arr);masks.append(mask);groups.append(i)
            # Real clip labels are never used to label candidate excerpts.
            labels.append(float(overlaps_contact(c,start,end)) if 'synthetic' in c['tags'] else -1.)
            info.append(dict(case=c['id'],center=m['seconds'],start=start,end=end,proposal_logit=m['logit']))
        progress.write(completed=i+1,current_case=c['id'])
    return dict(x=torch.stack(features),mask=torch.stack(masks),y=torch.tensor(labels),groups=np.array(groups),info=info,cases=cases)


@torch.inference_mode()
def score(model,data,mu,sigma):
    model.eval();out=[]
    for i in range(0,len(data['x']),32):
        x=(data['x'][i:i+32].cuda().float()-mu)/sigma
        mask=data['mask'][i:i+32].cuda()
        x=x*mask[:,:,None,None]
        out.extend(model(x,mask).cpu().tolist())
    out=np.array(out)
    return np.array([max(out[data['groups']==i]) for i in range(len(data['cases']))]),out


def main():
    p=argparse.ArgumentParser();p.add_argument('--root',required=True);a=p.parse_args()
    root=Path(a.root);out=root/'features/candidate-verifier-v1';out.mkdir(exist_ok=True)
    progress_path=root/'features/vjepa-dashboard/progress.json'
    torch.set_num_threads(4);torch.manual_seed(0);np.random.seed(0)
    cache=root/'features/vjepa21-vitl-384-15fps-f16s16'
    realcache=root/'features/vjepa21-vitl-384-15fps-f16s16-real48'
    proposer,ck=tr.load_head(str(cache/'temporal.pt'),'cuda');meta=store.read_meta(str(cache))
    assert store.read_meta(str(realcache))==meta
    synth=store.dataset_cases(str(root/'datasets/synthetic-train-val-600.json'))
    splits={'train':[c for c in synth if 'holdout' not in c['tags']], 'val':[c for c in synth if 'holdout' in c['tags']], 'real':store.dataset_cases(str(root/'real-eval/datasets/real-reviewed-corrected.json'))}
    assert len(splits['train'])==500 and len(splits['val'])==100
    data={}
    for split,cases in splits.items():
        writer=Writer(str(progress_path),'prepare-candidates',total=len(cases),completed=0,head='candidate-verifier',split=split)
        data[split]=prepare(cases,realcache if split=='real' else cache,proposer,meta,writer)
        writer.complete(completed=len(cases))
        print('prepared',split,len(data[split]['x']),'positive candidates',int((data[split]['y']==1).sum()),flush=True)
    mu=proposer.mu.detach();sigma=proposer.sigma.detach().clamp_min(1e-6)
    model=Verifier().cuda();opt=torch.optim.AdamW(model.parameters(),lr=3e-4,weight_decay=.05)
    writer=Writer(str(progress_path),'train',total=40,completed=0,head='candidate-verifier')
    best=-1;history=[]
    for epoch in range(40):
        model.train();losses=[]
        for idx in torch.randperm(len(data['train']['x'])).split(32):
            x=(data['train']['x'][idx].cuda().float()-mu)/sigma
            mask=data['train']['mask'][idx].cuda();x=(x+torch.randn_like(x)*.02)*mask[:,:,None,None]
            y=data['train']['y'][idx].cuda()
            loss=F.binary_cross_entropy_with_logits(model(x,mask),y)
            opt.zero_grad();loss.backward();torch.nn.utils.clip_grad_norm_(model.parameters(),1);opt.step();losses.append(loss.item())
        vs,_=score(model,data['val'],mu,sigma)
        auc=tr.auc(vs,[c['contact'] for c in splits['val']])
        history.append(dict(epoch=epoch+1,loss=float(np.mean(losses)),val_auc=auc))
        if auc>best:
            best=auc;torch.save(dict(state=model.state_dict(),mu=mu.cpu(),sigma=sigma.cpu(),epoch=epoch+1,meta=meta,history=history.copy()),out/'verifier.pt')
        writer.write(completed=epoch+1,history=history)
        print(history[-1],flush=True)
    saved=torch.load(out/'verifier.pt',weights_only=False);model.load_state_dict(saved['state'])
    for split in ['val','real']:
        cs,ps=score(model,data[split],mu,sigma)
        report=dict(head='candidate-verifier',selected_epoch=saved['epoch'],selection='highest synthetic validation clip AUC; first epoch wins ties',threshold=.5,
            auc=tr.auc(cs,[c['contact'] for c in splits[split]]),
            cases=[dict(case=c['id'],label=c['contact'],score=float(torch.sigmoid(torch.tensor(cs[i],dtype=torch.float64))),logit=float(cs[i]),tags=c['tags']) for i,c in enumerate(splits[split])],
            candidates=[dict(m,score=float(torch.sigmoid(torch.tensor(s,dtype=torch.float64))),logit=float(s)) for m,s in zip(data[split]['info'],ps)],history=history,
            notes='Synthetic-only candidate verifier; 3-second cached region sequences; seed 0; max over 3 proposals; no real fitting or threshold selection')
        (out/f'evaluate-{split}-verifier.json').write_text(json.dumps(report,indent=2)+'\n')
        print(split,'AUC',report['auc'],flush=True)
    writer.complete(completed=40,result=str(out/'evaluate-real-verifier.json'))
    print('VERIFIER COMPLETE',flush=True)

if __name__=='__main__':main()
