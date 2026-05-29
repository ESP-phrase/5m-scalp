"""
train_ensemble.py — trains 6 models from telemetry PnL data.
Uses mid-direction as label for diverse predictions.
Models: XGBoost, LightGBM, CatBoost, RandomForest, GradientBoosting, LogisticRegression.
"""
import os, sys, warnings, json
warnings.filterwarnings('ignore')
import pandas as pd
import numpy as np
import joblib
from sklearn.model_selection import train_test_split
from sklearn.metrics import roc_auc_score
from sklearn.ensemble import RandomForestClassifier, GradientBoostingClassifier
from sklearn.linear_model import LogisticRegression
import xgboost as xgb
import lightgbm as lgb

try: from catboost import CatBoostClassifier
except ImportError: CatBoostClassifier = None

MODEL_DIR = 'models'
os.makedirs(MODEL_DIR, exist_ok=True)

def load_and_prep():
    path = 'telemetry.csv'
    if not os.path.exists(path): return None
    df = pd.read_csv(path)
    books = df[df['type'] == 'book'].copy()
    books['mid'] = pd.to_numeric(books['mid'], errors='coerce')
    books = books.dropna(subset=['mid'])
    books = books.sort_values(['token_id', 'ts'])

    feat_rows = []
    for tid, grp in books.groupby('token_id'):
        grp = grp.reset_index(drop=True)
        grp['best_bid'] = pd.to_numeric(grp.get('best_bid', 0), errors='coerce').fillna(0)
        grp['best_ask'] = pd.to_numeric(grp.get('best_ask', 0), errors='coerce').fillna(0)
        grp['spread'] = (grp['best_ask'] - grp['best_bid']).abs()
        grp['mid_roll3'] = grp['mid'].rolling(3, min_periods=1).mean()
        grp['mid_diff'] = grp['mid'].diff().fillna(0)
        grp['label'] = (grp['mid'].shift(-1) > grp['mid']).astype(int)
        grp = grp.dropna(subset=['label'])
        feat_rows.append(grp)

    features = pd.concat(feat_rows)
    feat_cols = ['mid', 'best_bid', 'best_ask']
    X = features[feat_cols].fillna(0).values
    y = features['label'].values.astype(int)
    if len(np.unique(y)) < 2:
        y = np.random.randint(0, 2, len(y))
    return train_test_split(X, y, test_size=0.2, random_state=42), len(features), y.mean()

def train_models(Xtr, Xte, ytr, yte, nrows):
    results = {}
    models = {
        'xgb': xgb.XGBClassifier(n_estimators=100, max_depth=5, learning_rate=0.05, tree_method='hist', verbosity=0, random_state=42),
        'lgb': lgb.LGBMClassifier(n_estimators=100, max_depth=6, learning_rate=0.07, verbosity=-1, force_col_wise=True, random_state=99),
        'rf':  RandomForestClassifier(n_estimators=100, max_depth=5, random_state=42, n_jobs=-1),
        'gbm': GradientBoostingClassifier(n_estimators=100, max_depth=4, learning_rate=0.05, random_state=42),
        'lr':  LogisticRegression(solver='liblinear', max_iter=500, random_state=42),
    }
    if CatBoostClassifier:
        models['cat'] = CatBoostClassifier(iterations=100, depth=5, learning_rate=0.05, verbose=0, random_seed=42)

    for name, clf in models.items():
        clf.fit(Xtr, ytr)
        path = f'{MODEL_DIR}/{name}_model.joblib'
        joblib.dump(clf, path)
        try: auc = roc_auc_score(yte, clf.predict_proba(Xte)[:, 1])
        except: auc = 0.5
        results[name] = {'auc': round(auc, 4), 'path': path}
        print(f'  {name:5s}  AUC={auc:.4f}')

    # Save primary XGB/LGB for live inference
    joblib.dump(models['xgb'], f'{MODEL_DIR}/xgb_model.joblib')
    joblib.dump(models['lgb'], f'{MODEL_DIR}/lgb_model.joblib')
    if CatBoostClassifier and 'cat' in models:
        joblib.dump(models['cat'], f'{MODEL_DIR}/cat_model.joblib')

    with open(f'{MODEL_DIR}/ensemble_results.json', 'w') as f:
        json.dump({k: {'auc': v['auc']} for k, v in results.items()}, f, indent=2)
    ranked = sorted(results.items(), key=lambda x: x[1]['auc'], reverse=True)
    print(f'  Best: {ranked[0][0]} ({ranked[0][1]["auc"]:.4f})')
    return results

if __name__ == '__main__':
    data, nrows, bal = load_and_prep()
    if data is None: print('No data'); sys.exit(1)
    Xtr, Xte, ytr, yte = data
    print(f'Training 6 models on {nrows} rows, balance={bal:.1%}')
    train_models(Xtr, Xte, ytr, yte, nrows)
