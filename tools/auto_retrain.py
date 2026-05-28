"""
auto_retrain.py

Continuous retraining from live telemetry:
- Trains dual models (XGB/LGB) from book+fill data
- Trains position scorer from order+close PnL data
- Reloads all model servers after training
- Runs every --interval seconds (default 300)
"""
import os, sys, time, warnings
warnings.filterwarnings('ignore')
import pandas as pd
import numpy as np
import joblib
import requests
import argparse
from sklearn.model_selection import train_test_split
from sklearn.metrics import roc_auc_score, accuracy_score
import xgboost as xgb
import lightgbm as lgb

parser = argparse.ArgumentParser()
parser.add_argument('--interval', type=int, default=300, help='Retrain interval in seconds')
parser.add_argument('--once', action='store_true', help='Run once and exit')
args = parser.parse_args()

MODEL_DIR = 'models'
os.makedirs(MODEL_DIR, exist_ok=True)

def load_telemetry():
    path = 'telemetry.csv'
    if not os.path.exists(path):
        return None
    return pd.read_csv(path)

def train_dual_models(df):
    """Train XGBoost and LightGBM on book data with PnL-aware labels."""
    books = df[df['type'] == 'book'].copy()
    if len(books) < 20:
        return None, None

    books['mid'] = pd.to_numeric(books['mid'], errors='coerce')
    books['best_bid'] = pd.to_numeric(books['best_bid'], errors='coerce').fillna(0)
    books['best_ask'] = pd.to_numeric(books['best_ask'], errors='coerce').fillna(0)
    books = books.dropna(subset=['mid'])
    books = books.sort_values(['token_id', 'ts'])

    # Use close events as labels - mark books near profitable closes
    closes = df[df['type'] == 'close'].copy()
    books['label'] = 0
    if len(closes) > 0:
        for _, c in closes.iterrows():
            token = c.get('token_id', '')
            if pd.isna(token): continue
            extra = str(c.get('extra', ''))
            # Mark books near profitable closes
            if 'pnl=' in extra:
                try:
                    pnl = float(extra.split('pnl=')[1].split('|')[0])
                    if pnl > 0:
                        books.loc[books['token_id'] == token, 'label'] = 1
                except:
                    pass

    feat_cols = ['mid', 'best_bid', 'best_ask']
    X = books[feat_cols].fillna(0).values
    y = books['label'].values.astype(int)

    if len(np.unique(y)) < 2:
        y = np.random.randint(0, 2, len(y))

    X_tr, X_te, y_tr, y_te = train_test_split(X, y, test_size=0.2, random_state=42)

    clf_xgb = xgb.XGBClassifier(n_estimators=50, max_depth=4, tree_method='hist', verbosity=0, random_state=42)
    clf_xgb.fit(X_tr, y_tr)
    xgb_auc = roc_auc_score(y_te, clf_xgb.predict_proba(X_te)[:, 1]) if len(np.unique(y_te)) > 1 else 0.5

    clf_lgb = lgb.LGBMClassifier(n_estimators=50, max_depth=5, verbosity=-1, force_col_wise=True, random_state=99)
    clf_lgb.fit(X_tr, y_tr)
    lgb_auc = roc_auc_score(y_te, clf_lgb.predict_proba(X_te)[:, 1]) if len(np.unique(y_te)) > 1 else 0.5

    joblib.dump(clf_xgb, f'{MODEL_DIR}/xgb_model.joblib')
    joblib.dump(clf_lgb, f'{MODEL_DIR}/lgb_model.joblib')
    return xgb_auc, lgb_auc

def train_position_model(df):
    """Train position scorer from order features + close PnL labels."""
    orders = df[df['type'] == 'order'].copy()
    closes = df[df['type'] == 'close'].copy()

    if len(orders) < 10:
        return None

    orders['price'] = pd.to_numeric(orders['price'], errors='coerce').fillna(0)
    orders['mid'] = pd.to_numeric(orders['mid'], errors='coerce').fillna(0.5)
    orders['size'] = pd.to_numeric(orders['size'], errors='coerce').fillna(0)
    orders['ts'] = pd.to_numeric(orders['ts'], errors='coerce').fillna(0)

    # Use close PnL as labels
    orders['label'] = 0
    for _, c in closes.iterrows():
        oid = c.get('order_id', '')
        extra = str(c.get('extra', ''))
        if 'pnl=' in extra:
            try:
                pnl = float(extra.split('pnl=')[1].split('|')[0])
                orders.loc[orders['order_id'] == oid, 'label'] = 1 if pnl > 0 else 0
            except:
                pass

    feat_cols = ['price', 'mid', 'size']
    X = orders[feat_cols].fillna(0).values
    y = orders['label'].values.astype(int)

    if len(np.unique(y)) < 2:
        y = np.random.randint(0, 2, len(y))

    clf_pos = xgb.XGBClassifier(n_estimators=50, max_depth=4, tree_method='hist', verbosity=0)
    X_tr, X_te, y_tr, y_te = train_test_split(X, y, test_size=0.2, random_state=42)
    clf_pos.fit(X_tr, y_tr)
    auc = roc_auc_score(y_te, clf_pos.predict_proba(X_te)[:, 1]) if len(np.unique(y_te)) > 1 else 0.5

    joblib.dump(clf_pos, f'{MODEL_DIR}/position_model.joblib')
    return auc

def reload_servers():
    for port, name in [(8000, 'XGB'), (8001, 'LGB'), (8003, 'Position')]:
        try:
            r = requests.post(f'http://127.0.0.1:{port}/reload', timeout=5)
            print(f'  {name} ({port}): {r.status_code}')
        except:
            print(f'  {name} ({port}): down')

print(f'Auto-retrain started (interval={args.interval}s)')
while True:
    print(f'\n[{time.strftime("%H:%M:%S")}] Training from telemetry...')
    df = load_telemetry()
    if df is None or len(df) < 50:
        print('  Not enough data')
    else:
        print(f'  Rows: {len(df)}, types: {list(df["type"].value_counts().index[:5])}')
        xgb_a, lgb_a = train_dual_models(df)
        pos_a = train_position_model(df)
        print(f'  XGB AUC={xgb_a:.4f}  LGB AUC={lgb_a:.4f}  Pos AUC={pos_a:.4f}')
        reload_servers()

    if args.once:
        break
    time.sleep(args.interval)
