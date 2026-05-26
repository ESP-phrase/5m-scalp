const express = require('express');
const path = require('path');
const http = require('http');

const app = express();
const PORT = process.env.PORT || 3000;
const API_TARGET = process.env.API_TARGET || 'http://localhost:8400';

app.use(express.static(path.join(__dirname, 'public')));

// Proxy /api/* requests to the Go backend
app.use('/api', (req, res) => {
  const target = API_TARGET + req.originalUrl;
  const proxyReq = http.request(target, {
    method: req.method,
    headers: req.headers,
  }, (proxyRes) => {
    res.writeHead(proxyRes.statusCode, proxyRes.headers);
    proxyRes.pipe(res);
  });
  proxyReq.on('error', () => res.status(502).json({ error: 'Backend unreachable' }));
  if (req.method === 'POST' || req.method === 'PUT') {
    req.pipe(proxyReq);
  } else {
    proxyReq.end();
  }
});

// Proxy model servers (XGB and LGB) so dashboard can call same-origin
app.post('/model/xgb/predict', (req, res) => {
  const target = 'http://localhost:8000/predict';
  const proxyReq = http.request(target, { method: 'POST', headers: req.headers }, (proxyRes) => {
    res.writeHead(proxyRes.statusCode, proxyRes.headers);
    proxyRes.pipe(res);
  });
  proxyReq.on('error', () => res.status(502).json({ error: 'Model server unreachable' }));
  req.pipe(proxyReq);
});
app.post('/model/lgb/predict', (req, res) => {
  const target = 'http://localhost:8001/predict';
  const proxyReq = http.request(target, { method: 'POST', headers: req.headers }, (proxyRes) => {
    res.writeHead(proxyRes.statusCode, proxyRes.headers);
    proxyRes.pipe(res);
  });
  proxyReq.on('error', () => res.status(502).json({ error: 'Model server unreachable' }));
  req.pipe(proxyReq);
});


// Fallback to /events SSE proxy
app.get('/events', (req, res) => {
  const target = API_TARGET + '/api/events';
  const proxyReq = http.get(target, (proxyRes) => {
    res.writeHead(proxyRes.statusCode, {
      'Content-Type': 'text/event-stream',
      'Cache-Control': 'no-cache',
      'Connection': 'keep-alive',
      'Access-Control-Allow-Origin': '*',
    });
    proxyRes.pipe(res);
  });
  proxyReq.on('error', () => res.status(502).end());
  req.on('close', () => proxyReq.destroy());
});

app.get('/', (req, res) => {
  res.sendFile(path.join(__dirname, 'public', 'index.html'));
});

app.listen(PORT, () => {
  console.log(`Dashboard running at http://localhost:${PORT}`);
  console.log(`Proxying /api/* to ${API_TARGET}`);
});
