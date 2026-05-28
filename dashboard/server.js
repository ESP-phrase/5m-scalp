const express = require('express');
const path = require('path');
const http = require('http');

const app = express();
const PORT = process.env.PORT || 3002;
const TARGETS = {
  a: process.env.API_A || 'http://localhost:8401',
  b: process.env.API_B || 'http://localhost:8402',
  c: process.env.API_C || 'http://localhost:8403',
  main: process.env.API_TARGET || 'http://localhost:8400',
};

app.use((req, res, next) => {
  res.set('Cache-Control', 'no-store, no-cache, must-revalidate');
  res.set('Pragma', 'no-cache');
  res.set('Expires', '0');
  next();
});

app.use(express.static(path.join(__dirname, 'public')));

function proxyTo(base, req, res) {
  try {
    let b = (base || '').trim();
    if (!b.startsWith('http://') && !b.startsWith('https://')) b = 'http://' + b;
    const url = new URL(b);
    const proxyReq = http.request(url, { method: req.method, headers: req.headers }, (proxyRes) => {
      res.writeHead(proxyRes.statusCode, proxyRes.headers);
      proxyRes.pipe(res);
    });
    proxyReq.on('error', () => res.status(502).json({ error: 'Backend unreachable' }));
    if (req.method === 'POST' || req.method === 'PUT') req.pipe(proxyReq);
    else proxyReq.end();
  } catch (err) { res.status(502).json({ error: 'Invalid proxy target' }); }
}

// Per-instance proxies
app.use('/api-a', (req, res) => {
  const url = TARGETS.a + '/api' + req.url.replace('/api-a', '');
  proxyTo(url, req, res);
});
app.use('/api-b', (req, res) => {
  const url = TARGETS.b + '/api' + req.url.replace('/api-b', '');
  proxyTo(url, req, res);
});
app.use('/api-c', (req, res) => {
  const url = TARGETS.c + '/api' + req.url.replace('/api-c', '');
  proxyTo(url, req, res);
});
app.use('/api', (req, res) => {
  const url = TARGETS.main + req.url;
  proxyTo(url, req, res);
});

// SSE per-instance
['a','b','c'].forEach(slot => {
  app.get(`/events-${slot}`, (req, res) => {
    try {
      const base = TARGETS[slot];
      const targetUrl = new URL('/api/events', base).toString();
      const proxyReq = http.get(targetUrl, (proxyRes) => {
        res.writeHead(proxyRes.statusCode, { 'Content-Type': 'text/event-stream', 'Cache-Control': 'no-cache', 'Connection': 'keep-alive', 'Access-Control-Allow-Origin': '*' });
        const keepalive = setInterval(() => res.write(':keepalive\n\n'), 15000);
        proxyRes.on('data', (chunk) => res.write(chunk));
        proxyRes.on('end', () => { clearInterval(keepalive); res.end(); });
        proxyRes.on('error', () => { clearInterval(keepalive); res.end(); });
        req.on('close', () => { clearInterval(keepalive); proxyReq.destroy(); });
      });
      proxyReq.on('error', (err) => { if (!res.headersSent) res.status(502).json({ error: 'Backend unreachable' }); else res.end(); });
    } catch (err) { if (!res.headersSent) res.status(502).json({ error: 'Invalid proxy target' }); else res.end(); }
  });
});

// Model servers
app.post('/model/xgb/predict', (req, res) => {
  const pr = http.request('http://localhost:8000/predict', { method: 'POST', headers: req.headers }, (proxyRes) => {
    res.writeHead(proxyRes.statusCode, proxyRes.headers); proxyRes.pipe(res);
  });
  pr.on('error', () => res.status(502).json({ error: 'Model server unreachable' }));
  req.pipe(pr);
});
app.post('/model/lgb/predict', (req, res) => {
  const pr = http.request('http://localhost:8001/predict', { method: 'POST', headers: req.headers }, (proxyRes) => {
    res.writeHead(proxyRes.statusCode, proxyRes.headers); proxyRes.pipe(res);
  });
  pr.on('error', () => res.status(502).json({ error: 'Model server unreachable' }));
  req.pipe(pr);
});

app.get('/', (req, res) => res.sendFile(path.join(__dirname, 'public', 'index.html')));

if (require.main === module) {
  app.listen(PORT, () => {
    console.log(`Dashboard running at http://localhost:${PORT}`);
    console.log(`A: ${TARGETS.a}  B: ${TARGETS.b}  C: ${TARGETS.c}`);
  });
}
module.exports = app;
