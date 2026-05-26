@echo off
set MODEL_PATH=models/xgb_model.joblib
set PORT=8000
mkdir logs 2>nul
python tools/fastapi_model_server.py > logs/xgb.log 2>&1
