const API = '';
let currentSlot = 'a';
let depthChart = null;
let fills = [];
let orders = {};

const $ = (sel) => document.querySelector(sel);
const $$ = (sel) => document.querySelectorAll(sel);

// Tab switching
$$('.tab').forEach(tab => {
  tab.addEventListener('click', () => {
    $$('.tab').forEach(t => t.classList.remove('active'));
    tab.classList.add('active');
    currentSlot = tab.dataset.slot;
    fetchStats();
    fetchFills();
    fetchMarkets();
    connectSSE();
  });
});

// Comparison bar: poll all 3 instances every 3 seconds
async function updateCompareBar() {
  for (const s of ['a','b','c']) {
    try {
      const r = await fetch('/api-' + s + '/stats');
      if (!r.ok) continue;
      const d = await r.json();
      const el = $('#cmp-' + s).querySelector('span');
      el.textContent = '$' + (d.pnl.total || 0).toFixed(2);
      el.className = d.pnl.total >= 0 ? 'positive' : 'negative';
      const gasEl = $('#cmp-' + s).querySelector('small');
      const h = await fetch('/api-' + s + '/health').then(r => r.json()).catch(() => ({}));
      gasEl.textContent = 'gas:$' + (h.total_gas_pusd || 0).toFixed(2);
    } catch (e) {}
  }
}
setInterval(updateCompareBar, 3000);

function initChart() {
  const ctx = $('#depth-chart').getContext('2d');
  depthChart = new Chart(ctx, {
    type: 'bar',
    data: {
      labels: [],
      datasets: [
        {
          label: 'Bids',
          data: [],
          backgroundColor: 'rgba(16, 185, 129, 0.6)',
          borderColor: 'rgba(16, 185, 129, 1)',
          borderWidth: 1,
          borderRadius: 2,
        },
        {
          label: 'Asks',
          data: [],
          backgroundColor: 'rgba(239, 68, 68, 0.6)',
          borderColor: 'rgba(239, 68, 68, 1)',
          borderWidth: 1,
          borderRadius: 2,
        },
      ],
    },
    options: {
      responsive: true,
      maintainAspectRatio: false,
      plugins: {
        legend: { display: true, labels: { color: '#94a3b8' } },
      },
      scales: {
        x: {
          ticks: { color: '#94a3b8', maxRotation: 45 },
          grid: { color: 'rgba(148, 163, 184, 0.1)' },
        },
        y: {
          ticks: { color: '#94a3b8' },
          grid: { color: 'rgba(148, 163, 184, 0.1)' },
        },
      },
    },
  });
}

function updateDepthChart(bids, asks) {
  if (!depthChart) return;

  const labels = [];
  const bidData = [];
  const askData = [];

  for (const b of bids || []) {
    labels.push(b.price);
    bidData.push(b.size);
  }
  for (const a of asks || []) {
    labels.push(a.price);
    askData.push(a.size);
  }

  depthChart.data.labels = labels;
  depthChart.data.datasets[0].data = bidData;
  depthChart.data.datasets[1].data = askData;
  depthChart.update('none');
}

function updateBookInfo(data) {
  $('#best-bid').textContent = data.best_bid ? data.best_bid.toFixed(4) : '--';
  $('#best-ask').textContent = data.best_ask ? data.best_ask.toFixed(4) : '--';
  $('#spread').textContent = data.spread ? data.spread.toFixed(4) : '--';
}

function updateOrders(openOrders, midpoints) {
  const tbody = $('#orders-body');
  if (!openOrders || openOrders.length === 0) {
    tbody.innerHTML = '<tr><td colspan="8" class="empty">No open orders</td></tr>';
    return;
  }

  tbody.innerHTML = openOrders.map(o => {
    const mid = midpoints ? midpoints[o.token_id] : 0;
    const orderPnl = mid ? (o.side === 'BUY' ? (mid - o.price) * o.filled : (o.price - mid) * o.filled) : 0;
    const pnlClass = orderPnl >= 0 ? 'positive' : 'negative';
    return `
    <tr>
      <td class="mono">${o.id}</td>
      <td><span class="badge ${o.side === 'BUY' ? 'badge-green' : 'badge-red'}">${o.side}</span></td>
      <td class="mono">${o.price.toFixed(4)}</td>
      <td class="mono">${o.size.toFixed(2)}</td>
      <td class="mono">${o.filled.toFixed(2)}</td>
      <td class="mono ${pnlClass}">$${orderPnl.toFixed(2)}</td>
      <td><span class="badge badge-blue">${o.status}</span></td>
      <td><button class="btn btn-small btn-red" onclick="cancelOrder('${o.id}')">Close</button></td>
    </tr>`;
  }).join('');
}

function getApiPath(path) {
  return '/api-' + currentSlot + path;
}

function cancelOrder(orderId) {
  fetch(getApiPath('/orders/' + orderId + '/cancel'), { method: 'POST' })
    .then(r => r.json())
    .then(d => { addLog(d.ok ? 'Order cancelled: ' + orderId : 'Cancel failed: ' + orderId); fetchStats(); })
    .catch(() => addLog('Cancel failed: ' + orderId));
}

function addFill(fill) {
  fills.unshift(fill);
  if (fills.length > 50) fills.pop();

  const tbody = $('#fills-body');
  const rows = fills.map(f => `
    <tr>
      <td class="mono">${f.time || new Date().toISOString()}</td>
      <td class="mono">${f.order_id || ''}</td>
      <td><span class="badge ${f.side === 'BUY' ? 'badge-green' : 'badge-red'}">${f.side}</span></td>
      <td class="mono">${f.price.toFixed(4)}</td>
      <td class="mono">${f.size.toFixed(2)}</td>
    </tr>
  `).join('');
  tbody.innerHTML = rows;
}

function updatePnl(state) {
  $('#pnl-realized').textContent = '$' + (state.realized || 0).toFixed(2);
  $('#pnl-unrealized').textContent = '$' + (state.unrealized || 0).toFixed(2);
  $('#pnl-total').textContent = '$' + (state.total || 0).toFixed(2);
  $('#fill-count').textContent = state.fill_count || 0;
  $('#fee-rate').textContent = (state.fee_rate || 0.1).toFixed(2) + '%';

  const bankroll = state.bankroll || 200;
  const realized = state.realized || 0;
  const total = state.total || 0;
  const avail = bankroll + realized;
  const totalBal = bankroll + total;
  $('#pnl-balance').textContent = '$' + avail.toFixed(2);
  $('#pnl-total-balance').textContent = '$' + totalBal.toFixed(2);

  const el = $('#pnl-total');
  el.className = 'stat-value ' + (total >= 0 ? 'positive' : 'negative');
}

function addLog(msg) {
  const container = $('#log-container');
  const line = document.createElement('div');
  line.className = 'log-line';
  line.textContent = `[${new Date().toLocaleTimeString()}] ${msg}`;
  container.appendChild(line);
  container.scrollTop = container.scrollHeight;

  while (container.children.length > 200) {
    container.removeChild(container.firstChild);
  }
}

function setApiStatus(ok) {
  const dot = $('#api-dot');
  const text = $('#api-text');
  dot.className = 'status-dot ' + (ok ? 'connected' : 'disconnected');
  text.textContent = ok ? 'API Connected' : 'API Disconnected';
}

function setRunning(running) {
  $('#btn-start').disabled = running;
  $('#btn-stop').disabled = !running;
  const dot = $('#status-dot');
  dot.className = 'status-dot ' + (running ? 'running' : 'stopped');
  $('#status-text').textContent = running ? 'Running' : 'Stopped';
}

function connectSSE() {
  const es = new EventSource('/events-' + currentSlot);

  es.addEventListener('book', (e) => {
    try {
      const msg = JSON.parse(e.data);
      const d = msg.data;
      updateDepthChart(d.bids, d.asks);
      updateBookInfo(d);
    } catch (err) {}
  });

  es.addEventListener('order', (e) => {
    try {
      const msg = JSON.parse(e.data);
    } catch (err) {}
    fetchStats();
  });

  es.addEventListener('fill', (e) => {
    try {
      const msg = JSON.parse(e.data);
      addFill(msg.data);
    } catch (err) {}
  });

  es.addEventListener('pnl', (e) => {
    try {
      const msg = JSON.parse(e.data);
      updatePnl(msg.data);
    } catch (err) {}
  });

  es.addEventListener('trade', (e) => {
    try {
      const msg = JSON.parse(e.data);
      addLog(`Trade: ${msg.data.side} ${msg.data.size} @ ${msg.data.price} on ${msg.data.asset_id?.slice(0, 10)}...`);
    } catch (err) {}
  });

  es.addEventListener('log', (e) => {
    try {
      const msg = JSON.parse(e.data);
      addLog(msg.data.message);
    } catch (err) {}
  });

  es.onerror = () => {
    setTimeout(connectSSE, 3000);
  };
}

async function fetchStats() {
  try {
    const resp = await fetch(getApiPath('/stats'));
    if (!resp.ok) { addLog(`Stats API error: ${resp.status}`); return; }
    const data = await resp.json();
    setApiStatus(true);
    updatePnl(data.pnl);
    updateOrders(data.open_orders, data.midpoints);
    setRunning(data.running);
    $('#last-updated').textContent = new Date().toLocaleTimeString();
    if (data.best_bid || data.best_ask) {
      updateDepthChart(data.bids || [], data.asks || []);
      updateBookInfo({ best_bid: data.best_bid, best_ask: data.best_ask, spread: data.spread });
    }
  } catch (err) {
    setApiStatus(false);
    addLog(`Stats fetch failed: ${err.message}`);
  }
}

async function fetchFills() {
  try {
    const resp = await fetch(getApiPath('/fills'));
    if (!resp.ok) return;
    const data = await resp.json();
    const fillsArr = Array.isArray(data) ? data : (data.fills || []);
    fills = fillsArr.slice(0, 50);
    const tbody = $('#fills-body');
    if (fills.length === 0) {
      tbody.innerHTML = '<tr><td colspan="5" class="empty">No fills yet</td></tr>';
    } else {
      tbody.innerHTML = fills.map(f => `
        <tr>
          <td class="mono">${f.time || ''}</td>
          <td class="mono">${f.order_id || ''}</td>
          <td><span class="badge ${f.side === 'BUY' ? 'badge-green' : 'badge-red'}">${f.side || ''}</span></td>
          <td class="mono">${(f.price || 0).toFixed(4)}</td>
          <td class="mono">${(f.size || 0).toFixed(2)}</td>
        </tr>
      `).join('');
    }
    $('#last-updated').textContent = new Date().toLocaleTimeString();
  } catch (err) {}
}

async function fetchMarkets() {
  try {
    const resp = await fetch(getApiPath('/markets'));
    if (!resp.ok) return;
    const markets = await resp.json();
    const tbody = $('#markets-body');
    if (!markets || markets.length === 0) {
      tbody.innerHTML = '<tr><td colspan="2" class="empty">No markets found</td></tr>';
      return;
    }
    tbody.innerHTML = markets.slice(0, 20).map(m => `
      <tr>
        <td>${m.question}</td>
        <td class="mono">${(m.midpoint || 0).toFixed(4)}</td>
      </tr>
    `).join('');
  } catch (err) {}
}

async function apiCall(method, path, body) {
  try {
    const resp = await fetch(getApiPath(path), {
      method,
      headers: body ? { 'Content-Type': 'application/json' } : {},
      body: body ? JSON.stringify(body) : undefined,
    });
    if (!resp.ok) {
      addLog(`API error: ${method} ${path} → ${resp.status}`);
    }
    fetchStats();
  } catch (err) {
    addLog(`API unreachable: ${err.message}`);
  }
}

function closeAllOrders() {
  $('#orders-body').innerHTML = '<tr><td colspan="8" class="empty">Closing...</td></tr>';
  fetch(getApiPath('/orders/cancel-all'), { method: 'POST' })
    .then(r => r.json())
    .then(d => { if (d.ok) addLog('Closed ' + (d.count||'all') + ' orders | $' + (d.realized||0).toFixed(2)); });
  setTimeout(() => fetchStats(), 100);
}

$('#btn-start').addEventListener('click', () => {
  addLog('Starting bot...');
  apiCall('POST', '/bot/start');
});
$('#btn-stop').addEventListener('click', () => {
  addLog('Stopping bot...');
  apiCall('POST', '/bot/stop');
});
$('#strategy-select').addEventListener('change', (e) => {
  apiCall('POST', '/bot/strategy', { name: e.target.value });
});
$('#btn-close-all').addEventListener('click', closeAllOrders);

$('#btn-panic').addEventListener('click', () => {
  addLog('PANIC: stopping all instances...');
  ['a','b','c'].forEach(s => {
    fetch('/api-' + s + '/panic', { method: 'POST' })
      .then(r => r.json())
      .then(d => { if (d.ok) addLog('Panic ' + s.toUpperCase() + ': ' + (d.count||0) + ' orders closed, $' + (d.realized||0).toFixed(2)); })
      .catch(() => addLog('Panic ' + s.toUpperCase() + ': failed'));
  });
  fetchStats();
});

async function callModelProxy(path, features) {
  try {
    const resp = await fetch(path, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ features: [features] }),
    });
    if (!resp.ok) return null;
    const data = await resp.json();
    return data;
  } catch (err) { return null; }
}

async function updateModelPanels() {
  const bestBid = parseFloat($('#best-bid').textContent) || 0;
  const bestAsk = parseFloat($('#best-ask').textContent) || 0;
  const mid = bestBid && bestAsk ? (bestBid + bestAsk) / 2 : (parseFloat($('#pnl-total-balance').textContent.replace('$','')) || 0);
  const features = [mid, bestBid || mid, bestAsk || mid];

  const lgb = await callModelProxy('/model/lgb/predict', features);
  if (lgb && lgb.predictions) {
    $('#model-lgb-pred').textContent = (lgb.predictions[0]).toFixed(3);
    $('#model-lgb-lat').textContent = (lgb.latency_ms || 0).toFixed(1);
  }
  const xgb = await callModelProxy('/model/xgb/predict', features);
  if (xgb && xgb.predictions) {
    $('#model-xgb-pred').textContent = (xgb.predictions[0]).toFixed(3);
    $('#model-xgb-lat').textContent = (xgb.latency_ms || 0).toFixed(1);
  }
}

setInterval(updateModelPanels, 1000);
setInterval(fetchStats, 250);
setInterval(fetchFills, 250);
setInterval(fetchMarkets, 30000);

async function checkHealth() {
  try {
    const resp = await fetch(getApiPath('/health'));
    if (!resp.ok) return;
    const h = await resp.json();
    if (h.last_fill) {
      const age = (Date.now() - new Date(h.last_fill).getTime()) / 1000;
      if (age > 30) addLog('STALL: no fills in ' + Math.round(age) + 's');
    }
    if (!h.running) addLog('Bot stopped');
    $('#lat-ws').textContent = (h.ws_avg_ms || 0).toFixed(1);
    $('#lat-e2e').textContent = (h.e2e_avg_ms || 0).toFixed(1);
    $('#lat-close').textContent = (h.close_avg_ms || 0).toFixed(1);
    $('#lat-gas').textContent = '$' + (h.gas_per_fill || 0.02).toFixed(2) + ' | $' + (h.total_gas_pusd || 0).toFixed(2);
    if (h.reality_scores && h.reality_scores.length > 0) {
      const avg = h.reality_scores.reduce((s, r) => s + r.score_pct, 0) / h.reality_scores.length;
      $('#lat-reality').textContent = avg.toFixed(0) + '%';
    }
  } catch (err) {}
}
setInterval(checkHealth, 5000);

try {
  setApiStatus(false);
  initChart();
  connectSSE();
  fetchStats();
  fetchFills();
  fetchMarkets();

  $('#btn-close-lgb').addEventListener('click', () => {
    addLog('Close all (LGB)');
    closeAllOrders();
  });
  $('#btn-close-xgb').addEventListener('click', () => {
    addLog('Close all (XGB)');
    closeAllOrders();
  });

  addLog('Dashboard connected — paper trading mode');
} catch (e) {
  console.error('Dashboard init error:', e);
  addLog('Init error: ' + e.message);
}
