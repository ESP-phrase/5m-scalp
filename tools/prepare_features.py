"""
prepare_features.py

Read telemetry.csv and produce features.csv with time-series safe split and labels.
"""

import pandas as pd
import numpy as np
import os
import argparse

parser = argparse.ArgumentParser()
parser.add_argument('--in', dest='infile', default='telemetry.csv')
parser.add_argument('--out', dest='outfile', default='data/features.csv')
parser.add_argument('--horizon_s', dest='horizon', type=float, default=10.0)
parser.add_argument('--min_rows', dest='min_rows', type=int, default=50)
args = parser.parse_args()

if not os.path.exists(args.infile):
    print('input telemetry not found:', args.infile)
    raise SystemExit(1)

os.makedirs(os.path.dirname(args.outfile), exist_ok=True)

df = pd.read_csv(args.infile)
# keep book rows and fills for mid
books = df[df['type'] == 'book'].copy()
books['ts'] = pd.to_numeric(books['ts'], errors='coerce')
books['mid'] = pd.to_numeric(books['mid'], errors='coerce')
books['best_bid'] = pd.to_numeric(books['best_bid'], errors='coerce')
books['best_ask'] = pd.to_numeric(books['best_ask'], errors='coerce')
books = books.dropna(subset=['ts','mid'])

# window features per token
rows = []
for token, g in books.groupby('token_id'):
    g = g.sort_values('ts').reset_index(drop=True)
    if len(g) < args.min_rows:
        continue
    g['mid_1'] = g['mid'].shift(1)
    g['mid_2'] = g['mid'].shift(2)
    g['mid_diff1'] = g['mid'] - g['mid_1']
    g['mid_diff2'] = g['mid_1'] - g['mid_2']
    g['spread'] = g['best_ask'] - g['best_bid']
    g['ma3'] = g['mid'].rolling(3).mean()
    g['std3'] = g['mid'].rolling(3).std().fillna(0)
    # future mid after horizon seconds
    horizon_ms = int(args.horizon * 1000)
    g['future_mid'] = g['mid'].shift(-1)
    # label: future_mid > mid -> 1 else 0 (simple)
    g['label'] = (g['future_mid'] > g['mid']).astype(int)

    feats = g[['ts','token_id','mid','best_bid','best_ask','spread','mid_diff1','mid_diff2','ma3','std3','label']].dropna()
    rows.append(feats)

if len(rows) == 0:
    print('no tokens with enough rows to build features')
    raise SystemExit(1)

out = pd.concat(rows, ignore_index=True)
out.to_csv(args.outfile, index=False)
print('wrote', args.outfile, 'rows', len(out))
