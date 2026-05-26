"""
train_scheduler.py

Periodically retrain quick dummy model and notify model servers to reload.
"""

import time
import subprocess
import requests
import os

MODEL_XGB = 'models/xgb_model.joblib'
MODEL_LGB = 'models/lgb_model.joblib'

while True:
    try:
        # run quick training
        subprocess.run(['python', 'tools/quick_train_dummy.py'], check=True)
        # notify servers
        try:
            requests.post('http://127.0.0.1:8000/reload', timeout=5)
        except Exception:
            pass
        try:
            requests.post('http://127.0.0.1:8001/reload', timeout=5)
        except Exception:
            pass
    except Exception as e:
        print('Train scheduler error:', e)
    time.sleep(60)
