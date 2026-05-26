@echo off
REM run_experiment.bat <hours>
SET HOURS=%1
IF "%HOURS%"=="" SET HOURS=24
if not exist logs mkdir logs
set LOGFILE=logs\experiment_%HOURS%h_%DATE:~10,4%%DATE:~4,2%%DATE:~7,2%.log
echo Running live collection for %HOURS% hours... > %LOGFILE% 2>&1
REM Sleep for HOURS hours (convert to seconds)
set /a SLEEP_S=%HOURS%*3600
echo Sleeping for %SLEEP_S% seconds... >> %LOGFILE% 2>&1
powershell -Command "Start-Sleep -Seconds %SLEEP_S%" >> %LOGFILE% 2>&1

echo Preparing features... >> %LOGFILE% 2>&1
python tools/prepare_features.py --in telemetry.csv --out data/features.csv --horizon_s 10 >> %LOGFILE% 2>&1

echo Training models... >> %LOGFILE% 2>&1
python tools/train_and_tune.py --data data/features.csv --out models --n_trials 20 >> %LOGFILE% 2>&1

echo Running backtests and generating report... >> %LOGFILE% 2>&1
python tools/compare_report.py >> %LOGFILE% 2>&1

echo Experiment complete. Report at report.json >> %LOGFILE% 2>&1
echo Done. Log: %LOGFILE%

