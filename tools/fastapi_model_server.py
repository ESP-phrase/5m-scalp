from fastapi import FastAPI, HTTPException
from pydantic import BaseModel
import time
import uvicorn
import numpy as np
import joblib
import os

app = FastAPI()
model = None

class PredictRequest(BaseModel):
    features: list

@app.on_event("startup")
def load_model_on_startup():
    global model
    path = os.environ.get('MODEL_PATH', 'models/xgb_model.joblib')
    model = joblib.load(path)
    app.state.model_type = 'sklearn'
    print('Loaded model', path)

@app.post('/predict')
def predict(req: PredictRequest):
    if model is None:
        raise HTTPException(status_code=500, detail='Model not loaded')
    X = np.array(req.features)
    start = time.time()
    preds = model.predict_proba(X)[:,1]
    latency = (time.time() - start) * 1000.0
    return {'predictions': preds.tolist(), 'latency_ms': latency}

@app.post('/reload')
def reload_model():
    global model
    path = os.environ.get('MODEL_PATH', 'models/xgb_model.joblib')
    try:
        model = joblib.load(path)
        return {'ok': True, 'model': path}
    except Exception as e:
        raise HTTPException(status_code=500, detail=str(e))

if __name__ == '__main__':
    uvicorn.run(app, host='127.0.0.1', port=int(os.environ.get('PORT', 8000)))
