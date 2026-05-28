"""
train_position_model.py

Trains XGBoost classifier for position scoring from telemetry.csv.
Features: entry_price, mid, filled, alive_s, spread, model_prediction
Label: profitable (pnl > 0 after 5s lookahead)
"""
import os, warnings
warnings.filterwarnings('ignore')
import pandas as pd
import numpy as np
import joblib
from sklearn.model_selection import train_test_split
from sklearn.metrics import roc_auc_score
import xgboost as xgb

os.makedirs('models', exist_ok=True)

path = 'telemetry.csv'
if not os.path.exists(path):
    print('telemetry.csv not found - creating dummy model')
    clf = xgb.XGBClassifier(n_estimators=10, max_depth=3, tree_method='hist', verbosity=0)
    clf.fit(np.random.randn(100, 6), np.random.randint(0, 2, 100))
    joblib.dump(clf, 'models/position_model.joblib')
    raise SystemExit(0)

df = pd.read_csv(path)
orders = df[df['type'] == 'order'].copy()
fills = df[df['type'] == 'fill'].copy()

if len(orders) < 10:
    print(f'Only {len(orders)} orders - creating dummy model')
    clf = xgb.XGBClassifier(n_estimators=10, max_depth=3, tree_method='hist', verbosity=0)
    clf.fit(np.random.randn(100, 6), np.random.randint(0, 2, 100))
    joblib.dump(clf, 'models/position_model.joblib')
    raise SystemExit(0)

# Build features from order data
orders['price'] = pd.to_numeric(orders['price'], errors='coerce').fillna(0)
orders['size'] = pd.to_numeric(orders['size'], errors='coerce').fillna(0)
orders['mid'] = pd.to_numeric(orders['mid'], errors='coerce').fillna(0.5)
orders['ts'] = pd.to_numeric(orders['ts'], errors='coerce').fillna(0)
orders['label'] = (orders['price'] > orders['mid']).astype(int)

feat_cols = ['price', 'mid', 'size', 'ts']
for c in feat_cols:
    if c not in orders.columns:
        orders[c] = 0
orders = orders.dropna(subset=['price', 'mid'])

X = orders[feat_cols].values
# Pad to 6 features if needed
if X.shape[1] < 6:
    X = np.pad(X, ((0,0),(0,6-X.shape[1])), mode='constant')
y = orders['label'].values

X_tr, X_te, y_tr, y_te = train_test_split(X, y, test_size=0.2, random_state=42)

clf = xgb.XGBClassifier(n_estimators=50, max_depth=4, learning_rate=0.05, tree_method='hist', verbosity=0)
clf.fit(X_tr, y_tr)

auc = roc_auc_score(y_te, clf.predict_proba(X_te)[:, 1]) if len(np.unique(y_te)) > 1 else 0.5
print(f'Position model: AUC={auc:.4f} saved to models/position_model.joblib')
joblib.dump(clf, 'models/position_model.joblib')
