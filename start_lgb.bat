@echo off
set MODEL_PATH=models/lgb_model.joblib
set PORT=8001
mkdir logs 2>nul
python tools/fastapi_model_server.py > logs/lgb.log 2>&1
