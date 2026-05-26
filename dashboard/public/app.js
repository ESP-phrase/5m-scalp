const API = '';

let depthChart = null;
let fills = [];
let orders = {};

const $ = (sel) => document.querySelector(sel);

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

function cancelOrder(orderId) {
  fetch(API + '/api/orders/' + orderId + '/cancel', { method: 'POST' })
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

function setRunning(running) {
  $('#btn-start').disabled = running;
  $('#btn-stop').disabled = !running;
  const dot = $('#status-dot');
  dot.className = 'status-dot ' + (running ? 'running' : 'stopped');
  $('#status-text').textContent = running ? 'Running' : 'Stopped';
}

function connectSSE() {
  const es = new EventSource('/events');

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
    const resp = await fetch(API + '/api/stats');
    if (!resp.ok) { addLog(`Stats API error: ${resp.status}`); return; }
    const data = await resp.json();
    updatePnl(data.pnl);
    updateOrders(data.open_orders, data.midpoints);
    setRunning(data.running);
  } catch (err) {
    addLog(`Stats fetch failed: ${err.message}`);
  }
}

async function fetchMarkets() {
  try {
    const resp = await fetch(API + '/api/markets');
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
    const resp = await fetch(API + path, {
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

$('#btn-start').addEventListener('click', () => {
  addLog('Starting bot...');
  apiCall('POST', '/api/bot/start');
});
$('#btn-stop').addEventListener('click', () => {
  addLog('Stopping bot...');
  apiCall('POST', '/api/bot/stop');
});
$('#strategy-select').addEventListener('change', (e) => {
  apiCall('POST', '/api/bot/strategy', { name: e.target.value });
});
$('#btn-close-all').addEventListener('click', closeAllOrders);

async function callModelProxy(path, features) {
  try {
    const resp = await fetch(API + path, {
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

$('#btn-close-lgb').addEventListener('click', () => { addLog('Close all (LGB) triggered'); closeAllOrders(); });
$('#btn-close-xgb').addEventListener('click', () => { addLog('Close all (XGB) triggered'); closeAllOrders(); });

setInterval(updateModelPanels, 1000);

initChart();
connectSSE();
fetchStats();
fetchMarkets();

setInterval(fetchStats, 500);
setInterval(fetchMarkets, 30000);

addLog('Dashboard connected — paper trading mode');
