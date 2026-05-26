"""
quick_train_dummy.py

Create quick logistic regression models from telemetry.csv for initial live predictions.
Saves models/xgb_model.joblib and models/lgb_model.joblib (both are same dummy model for now).
"""

import os
import pandas as pd
import numpy as np
from sklearn.linear_model import LogisticRegression
import joblib

os.makedirs('models', exist_ok=True)
path = 'telemetry.csv'
if not os.path.exists(path):
    print('telemetry.csv not found')
    raise SystemExit(1)

# Read telemetry and build a simple dataset: use successive book rows per token
df = pd.read_csv(path)
# Keep only book rows
books = df[df['type']=='book'].copy()
# parse mid
books['mid'] = pd.to_numeric(books['mid'], errors='coerce')
books = books.dropna(subset=['mid'])
# sort by ts
books = books.sort_values('ts')
# create label: future mid change > 0
books['next_mid'] = books['mid'].shift(-1)
books = books.dropna(subset=['next_mid'])
books['label'] = (books['next_mid'] > books['mid']).astype(int)
# features: mid, best_bid, best_ask
books['best_bid'] = pd.to_numeric(books['best_bid'], errors='coerce').fillna(books['mid'])
books['best_ask'] = pd.to_numeric(books['best_ask'], errors='coerce').fillna(books['mid'])
X = books[['mid','best_bid','best_ask']]
y = books['label']

if len(X) < 10:
    print('not enough data to train dummy model, need at least 10 rows')
    raise SystemExit(1)

clf = LogisticRegression(solver='liblinear')
clf.fit(X, y)
joblib.dump(clf, 'models/xgb_model.joblib')
joblib.dump(clf, 'models/lgb_model.joblib')
print('Saved models to models/xgb_model.joblib and models/lgb_model.joblib')
