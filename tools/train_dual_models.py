"""
train_dual_models.py

Train separate XGBoost and LightGBM classifiers from telemetry.csv.
Matches the 3 features the Go bot sends: [mid, best_bid, best_ask].
"""
import os, warnings
warnings.filterwarnings('ignore')

import pandas as pd
import numpy as np
import joblib
import requests
from sklearn.model_selection import train_test_split
from sklearn.metrics import roc_auc_score
import xgboost as xgb
import lightgbm as lgb

os.makedirs('models', exist_ok=True)

path = 'telemetry.csv'
if not os.path.exists(path):
    print('telemetry.csv not found')
    raise SystemExit(1)

df = pd.read_csv(path)
books = df[df['type'] == 'book'].copy()
if len(books) < 20:
    print(f'Only {len(books)} book rows, need 20+')
    raise SystemExit(1)

books['mid'] = pd.to_numeric(books['mid'], errors='coerce')
books['best_bid'] = pd.to_numeric(books['best_bid'], errors='coerce').fillna(0)
books['best_ask'] = pd.to_numeric(books['best_ask'], errors='coerce').fillna(0)
books = books.dropna(subset=['mid'])
books = books.sort_values(['token_id', 'ts'])

feat_rows = []
for tid, grp in books.groupby('token_id'):
    grp = grp.reset_index(drop=True)
    grp['label'] = (grp['mid'].shift(-1) > grp['mid']).astype(int)
    grp = grp.dropna(subset=['label'])
    feat_rows.append(grp)

features = pd.concat(feat_rows)
feat_cols = ['mid', 'best_bid', 'best_ask']

X = features[feat_cols].fillna(0).values
y = features['label'].values.astype(int)

print(f'Dataset: {len(X)} rows, pos={y.sum()} neg={len(y)-y.sum()}, balance={y.mean():.2%}')

X_tr, X_te, y_tr, y_te = train_test_split(X, y, test_size=0.2, random_state=42, stratify=y)

# XGBoost
clf_xgb = xgb.XGBClassifier(
    n_estimators=80, max_depth=4, learning_rate=0.05,
    subsample=0.8, colsample_bytree=0.9, random_state=42,
    scale_pos_weight=max(1, (len(y_tr)-y_tr.sum()) / max(1, y_tr.sum())),
    tree_method='hist', verbosity=0
)
clf_xgb.fit(X_tr, y_tr)
xgb_auc = roc_auc_score(y_te, clf_xgb.predict_proba(X_te)[:, 1])

# LightGBM
clf_lgb = lgb.LGBMClassifier(
    n_estimators=80, max_depth=5, learning_rate=0.07,
    subsample=0.85, colsample_bytree=0.8, random_state=99,
    scale_pos_weight=max(1, (len(y_tr)-y_tr.sum()) / max(1, y_tr.sum())),
    verbosity=-1, force_col_wise=True
)
clf_lgb.fit(X_tr, y_tr)
lgb_auc = roc_auc_score(y_te, clf_lgb.predict_proba(X_te)[:, 1])

# Save
joblib.dump(clf_xgb, 'models/xgb_model.joblib')
joblib.dump(clf_lgb, 'models/lgb_model.joblib')

print(f'XGBoost  -> auc={xgb_auc:.4f}  saved models/xgb_model.joblib')
print(f'LightGBM -> auc={lgb_auc:.4f}  saved models/lgb_model.joblib')

# Reload servers
for port, name in [(8000, 'XGB'), (8001, 'LGB')]:
    try:
        r = requests.post(f'http://127.0.0.1:{port}/reload', timeout=5)
        print(f'Reloaded {name} on port {port}: {r.json()}')
    except Exception as e:
        print(f'Reload {name} failed: {e}')

# Verify
sample = np.array([[0.50, 0.48, 0.52]])
p_xgb = float(clf_xgb.predict_proba(sample)[:, 1][0])
p_lgb = float(clf_lgb.predict_proba(sample)[:, 1][0])
print(f'Sanity: XGB={p_xgb:.4f}  LGB={p_lgb:.4f}  diff={abs(p_xgb-p_lgb):.4f}')
