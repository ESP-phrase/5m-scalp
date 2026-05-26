"""
compare_report.py

Run backtests for models in models/ and compare XGB vs LGB by collecting final PnL per fold.
Produces report.json with metrics and a simple paired t-test if scipy available.
"""

import subprocess
import glob
import json
import re
import os
import sys
import numpy as np


def run_backtest(data_path, model_path):
    cmd = [sys.executable, 'tools/backtest.py', '--data', data_path, '--model', model_path]
    p = subprocess.Popen(cmd, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True)
    out, _ = p.communicate()
    # parse Final PnL, Sharpe, Max Drawdown
    pnl = None
    sharpe = None
    mdd = None
    for line in out.splitlines():
        if 'Final PnL:' in line:
            try:
                pnl = float(line.split(':',1)[1].strip())
            except:
                pass
        if 'Sharpe:' in line:
            try:
                sharpe = float(line.split(':',1)[1].strip())
            except:
                pass
        if 'Max Drawdown:' in line:
            try:
                mdd = float(line.split(':',1)[1].strip())
            except:
                pass
    return {'pnl': pnl, 'sharpe': sharpe, 'mdd': mdd, 'raw': out}


def main():
    data = 'data/features.csv'
    xgb_models = sorted(glob.glob('models/xgb_fold*.model'))
    lgb_models = sorted(glob.glob('models/lgb_fold*.txt'))
    res = {'xgb': {}, 'lgb': {}}

    for m in xgb_models:
        print('Backtesting', m)
        r = run_backtest(data, m)
        res['xgb'][os.path.basename(m)] = r
    for m in lgb_models:
        print('Backtesting', m)
        r = run_backtest(data, m)
        res['lgb'][os.path.basename(m)] = r

    # summary arrays
    x_pnls = [v['pnl'] for v in res['xgb'].values() if v['pnl'] is not None]
    l_pnls = [v['pnl'] for v in res['lgb'].values() if v['pnl'] is not None]

    summary = {
        'xgb_mean_pnl': float(np.mean(x_pnls)) if x_pnls else None,
        'lgb_mean_pnl': float(np.mean(l_pnls)) if l_pnls else None,
        'xgb_std_pnl': float(np.std(x_pnls, ddof=1)) if len(x_pnls)>1 else None,
        'lgb_std_pnl': float(np.std(l_pnls, ddof=1)) if len(l_pnls)>1 else None,
        'xgb_counts': len(x_pnls),
        'lgb_counts': len(l_pnls),
    }

    # try paired t-test if lengths match
    try:
        from scipy import stats
        if len(x_pnls) == len(l_pnls) and len(x_pnls) > 1:
            tstat, pval = stats.ttest_rel(x_pnls, l_pnls)
            summary['paired_tstat'] = float(tstat)
            summary['paired_pval'] = float(pval)
    except Exception:
        pass

    out = {'results': res, 'summary': summary}
    with open('report.json', 'w') as f:
        json.dump(out, f, indent=2)
    print('Wrote report.json')

if __name__ == '__main__':
    main()
