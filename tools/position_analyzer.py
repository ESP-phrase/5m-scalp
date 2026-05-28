"""
position_analyzer.py

FastAPI server for AI-driven TP/SL recommendations and position scoring.
Loads XGBoost model from models/position_model.joblib.
Falls back to heuristic if no model available.

Endpoints:
  POST /positions  - score all open positions, recommend TP/SL
  POST /close      - decide whether to close specific positions
"""
import os, warnings
warnings.filterwarnings('ignore')

from fastapi import FastAPI
from pydantic import BaseModel
import numpy as np
import joblib
import uvicorn

app = FastAPI()
model = None
model_path = os.environ.get('POSITION_MODEL', 'models/position_model.joblib')

@app.on_event('startup')
def load_model():
    global model
    if os.path.exists(model_path):
        model = joblib.load(model_path)
        print(f'Loaded position model: {model_path}')
    else:
        print(f'No model at {model_path}, using heuristic fallback')

class PositionRequest(BaseModel):
    positions: list

class CloseRequest(BaseModel):
    orders: list

def heuristic_score(entry, mid, filled, side, alive_s):
    """Fallback when no ML model: higher score = better position."""
    if alive_s <= 0:
        alive_s = 1
    pnl = (mid - entry) * filled if side == 'BUY' else (entry - mid) * filled
    return float(pnl / (alive_s + 0.5))

def heuristic_tp(entry, mid, filled, side, spread):
    """TP recommendation based on spread and position size."""
    target = abs(spread) * 2.0 + 0.02
    return float(max(target, 1.0))

def heuristic_sl(entry, mid, filled, side):
    """SL recommendation based on position risk."""
    return float(max(1.5, filled * 0.05))

@app.post('/positions')
def analyze_positions(req: PositionRequest):
    results = []
    for pos in req.positions:
        entry = float(pos.get('entry_price', 0))
        mid = float(pos.get('mid', entry or 0.5))
        filled = float(pos.get('filled', 0))
        side = pos.get('side', 'BUY')
        alive = float(pos.get('alive_s', 1))
        spread = float(pos.get('spread', 0.02))
        pred = float(pos.get('model_pred', 0))

        if model:
            feats = np.array([[entry, mid, filled, alive, spread, pred]])
            score = float(np.clip(model.predict_proba(feats)[:, 1][0], 0, 10))
            tp = float(max(1.0, score * 0.5))
            sl = float(max(1.0, tp * 0.4))
        else:
            score = heuristic_score(entry, mid, filled, side, alive)
            tp = heuristic_tp(entry, mid, filled, side, spread)
            sl = heuristic_sl(entry, mid, filled, side)

        results.append({
            'token_id': pos.get('token_id', ''),
            'order_id': pos.get('order_id', ''),
            'score': round(score, 2),
            'recommended_tp': round(tp, 2),
            'recommended_sl': round(sl, 2),
        })
    return {'positions': results}

@app.post('/close')
def close_advice(req: CloseRequest):
    results = []
    for o in req.orders:
        pnl = float(o.get('current_pnl', 0))
        alive = float(o.get('alive_s', 1))
        filled_ratio = float(o.get('filled_ratio', 0))

        urgency = 0
        reason = 'hold'
        if pnl > 5 and alive > 30:
            urgency = 7
            reason = 'take profit'
        elif pnl < -3 and filled_ratio > 0.5:
            urgency = 9
            reason = 'cut loss'
        elif alive > 120:
            urgency = 4
            reason = 'stale position'

        action = 'close' if urgency >= 5 else 'hold'
        results.append({
            'order_id': o.get('order_id', ''),
            'action': action,
            'reason': reason,
            'urgency': urgency,
        })
    return {'orders': results}


@app.post('/reload')
def reload_model():
    global model
    if os.path.exists(model_path):
        model = joblib.load(model_path)
        return {'ok': True, 'model': model_path}
    return {'ok': False, 'error': 'model not found'}

if __name__ == '__main__':
    uvicorn.run(app, host='127.0.0.1', port=int(os.environ.get('PORT', 8003)))
