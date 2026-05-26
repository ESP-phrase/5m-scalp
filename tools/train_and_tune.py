"""
train_and_tune.py

GPU-aware training pipeline for XGBoost and LightGBM with Optuna fallback to CPU if GPU not available.
Usage:
  python tools/train_and_tune.py --data data/features.csv --out models/ --target label --n_trials 8
"""

import argparse
import os
import joblib
import json
import numpy as np
import pandas as pd
from sklearn.model_selection import TimeSeriesSplit
from sklearn.metrics import roc_auc_score
import optuna

# try import xgboost and lightgbm
try:
    import xgboost as xgb
    XGBOOST_AVAILABLE = True
except Exception:
    XGBOOST_AVAILABLE = False
try:
    import lightgbm as lgb
    LGB_AVAILABLE = True
except Exception:
    LGB_AVAILABLE = False


def load_data(path, ts_col=None):
    df = pd.read_csv(path)
    if ts_col and ts_col in df.columns:
        df = df.sort_values(ts_col).reset_index(drop=True)
    return df


def prepare_xy(df, target, drop_cols=None):
    drop_cols = drop_cols or []
    X = df.drop(columns=[target] + drop_cols, errors='ignore')
    y = df[target].values
    return X, y


def train_xgb(params, X_train, y_train, X_val, y_val):
    dtrain = xgb.DMatrix(X_train, label=y_train)
    dval = xgb.DMatrix(X_val, label=y_val)
    bst = xgb.train(params, dtrain, num_boost_round=200, evals=[(dval, 'valid')], early_stopping_rounds=20, verbose_eval=False)
    preds = bst.predict(dval)
    return bst, roc_auc_score(y_val, preds)


def train_lgb(params, X_train, y_train, X_val, y_val):
    dtrain = lgb.Dataset(X_train, label=y_train)
    dval = lgb.Dataset(X_val, label=y_val)
    # use callbacks for early stopping for newer lightgbm
    try:
        bst = lgb.train(params, dtrain, num_boost_round=200, valid_sets=[dval], callbacks=[lgb.early_stopping(20)])
    except Exception:
        bst = lgb.train(params, dtrain, num_boost_round=200, valid_sets=[dval])
    preds = bst.predict(X_val)
    return bst, roc_auc_score(y_val, preds)


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--data', required=True)
    parser.add_argument('--out', default='models')
    parser.add_argument('--target', default='label')
    parser.add_argument('--ts', default='ts')
    parser.add_argument('--n_trials', type=int, default=8)
    args = parser.parse_args()

    os.makedirs(args.out, exist_ok=True)
    df = load_data(args.data, ts_col=args.ts)
    drop_cols = [args.ts, 'token_id']
    X, y = prepare_xy(df, args.target, drop_cols=drop_cols)

    tss = TimeSeriesSplit(n_splits=3)
    splits = list(tss.split(X))

    results = {'xgb': [], 'lgb': []}

    for fold, (train_idx, valid_idx) in enumerate(splits):
        X_train, y_train = X.iloc[train_idx], y[train_idx]
        X_val, y_val = X.iloc[valid_idx], y[valid_idx]

        # XGBoost
        if XGBOOST_AVAILABLE:
            # detect GPU support
            use_gpu = False
            try:
                if 'gpu' in xgb.__version__ or hasattr(xgb, 'cuda'):
                    use_gpu = True
            except Exception:
                use_gpu = False
            params = {
                'verbosity': 0,
                'objective': 'binary:logistic',
                'eval_metric': 'auc',
            }
            if use_gpu:
                params.update({'tree_method': 'gpu_hist', 'predictor': 'gpu_predictor', 'gpu_id': 0})
            else:
                params.update({'tree_method': 'hist'})
            print('Training XGBoost fold', fold, 'gpu=', use_gpu)
            bst, auc = train_xgb(params, X_train, y_train, X_val, y_val)
            path = os.path.join(args.out, f'xgb_fold{fold}.model')
            bst.save_model(path)
            results['xgb'].append({'fold': fold, 'auc': auc, 'path': path})
            print('XGB fold', fold, 'auc', auc)

        # LightGBM
        if LGB_AVAILABLE:
            # detect GPU support
            use_gpu = False
            try:
                # lightgbm exposes gpu support via device argument only
                use_gpu = False
            except Exception:
                use_gpu = False
            params = {
                'objective': 'binary',
                'metric': 'auc',
                'verbosity': -1,
            }
            if use_gpu:
                params.update({'device': 'gpu', 'gpu_platform_id': 0, 'gpu_device_id': 0})
            print('Training LightGBM fold', fold, 'gpu=', use_gpu)
            bst, auc = train_lgb(params, X_train, y_train, X_val, y_val)
            path = os.path.join(args.out, f'lgb_fold{fold}.txt')
            bst.save_model(path)
            results['lgb'].append({'fold': fold, 'auc': auc, 'path': path})
            print('LGB fold', fold, 'auc', auc)

    with open(os.path.join(args.out, 'train_results.json'), 'w') as f:
        json.dump(results, f, indent=2)
    print('Training complete. Results saved to', args.out)

if __name__ == '__main__':
    main()
