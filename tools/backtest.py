"""
backtest.py

Run backtest using model predictions over features CSV and compute simple PnL metrics.
"""

import argparse
import pandas as pd
import numpy as np
import os
import joblib

try:
    import xgboost as xgb
except Exception:
    xgb = None
try:
    import lightgbm as lgb
except Exception:
    lgb = None


def load_model(path):
    if path.endswith('.txt'):
        # lightgbm
        if lgb is not None:
            bst = lgb.Booster(model_file=path)
            return bst, 'lgb'
    if path.endswith('.model'):
        # xgboost
        if xgb is not None:
            bst = xgb.Booster()
            bst.load_model(path)
            return bst, 'xgb'
    # try to detect by trying lgb then xgb
    if lgb is not None:
        try:
            bst = lgb.Booster(model_file=path)
            return bst, 'lgb'
        except Exception:
            pass
    if xgb is not None:
        try:
            bst = xgb.Booster()
            bst.load_model(path)
            return bst, 'xgb'
        except Exception:
            pass
    # fallback sklearn
    m = joblib.load(path)
    return m, 'sklearn'


def predict(model_tuple, X):
    model, kind = model_tuple
    if kind == 'lgb':
        return np.array(model.predict(X))
    if kind == 'xgb':
        dmat = xgb.DMatrix(X)
        return np.array(model.predict(dmat))
    # sklearn fallback
    try:
        return model.predict_proba(X)[:,1]
    except Exception:
        return np.array(model.predict(X))


def simulate_trades(preds, df, threshold=0.6, fee=0.001, size=1.0):
    cash = 0.0
    pos = 0.0
    pnl_curve = []
    for i, p in enumerate(preds):
        price = df.iloc[i]['mid']
        if p > threshold:
            pos += size
            cash -= price * size * (1 + fee)
        elif p < (1 - threshold):
            sell_size = min(pos, size)
            cash += price * sell_size * (1 - fee)
            pos -= sell_size
        total = cash + pos * price
        pnl_curve.append(total)
    return np.array(pnl_curve)


def sharpe(returns, rf=0.0):
    if len(returns) == 0:
        return 0.0
    mean = returns.mean()
    std = returns.std()
    if std == 0:
        return 0.0
    return (mean - rf) / std * np.sqrt(252)


def max_drawdown(series):
    peak = series[0]
    md = 0.0
    for v in series:
        if v > peak:
            peak = v
        dd = (peak - v) / (peak if peak != 0 else 1)
        if dd > md:
            md = dd
    return md


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--data', required=True)
    parser.add_argument('--model', required=True)
    parser.add_argument('--threshold', type=float, default=0.6)
    args = parser.parse_args()

    df = pd.read_csv(args.data)
    X = df.drop(columns=['label','ts','token_id'], errors='ignore')
    # keep 'mid' as a feature so predictions use same columns as training
    model = load_model(args.model)
    preds = predict(model, X)

    pnl_curve = simulate_trades(preds, df, threshold=args.threshold)
    returns = np.diff(pnl_curve)
    s = sharpe(pd.Series(returns))
    md = max_drawdown(pnl_curve)

    print('Final PnL:', pnl_curve[-1] if len(pnl_curve)>0 else 0.0)
    print('Sharpe:', s)
    print('Max Drawdown:', md)

if __name__ == '__main__':
    main()
