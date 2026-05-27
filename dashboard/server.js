const express = require('express');
const path = require('path');
const http = require('http');

const app = express();
const PORT = process.env.PORT || 3000;
const API_TARGET = process.env.API_TARGET || 'http://localhost:8400';

app.use((req, res, next) => {
  res.set('Cache-Control', 'no-store, no-cache, must-revalidate');
  res.set('Pragma', 'no-cache');
  res.set('Expires', '0');
  next();
});

app.use(express.static(path.join(__dirname, 'public')));

// Proxy /api/* requests to the Go backend
app.use('/api', (req, res) => {
  try {
    let base = (API_TARGET || '').trim();
    if (!base.startsWith('http://') && !base.startsWith('https://')) {
      base = 'http://' + base;
    }
    const targetUrl = new URL(req.originalUrl, base).toString();
    const proxyReq = http.request(targetUrl, {
      method: req.method,
      headers: req.headers,
    }, (proxyRes) => {
      res.writeHead(proxyRes.statusCode, proxyRes.headers);
      proxyRes.pipe(res);
    });
    proxyReq.on('error', (err) => {
      console.error('Proxy error:', err);
      res.status(502).json({ error: 'Backend unreachable' });
    });
    if (req.method === 'POST' || req.method === 'PUT') {
      req.pipe(proxyReq);
    } else {
      proxyReq.end();
    }
  } catch (err) {
    console.error('Proxy build error:', err);
    res.status(502).json({ error: 'Invalid proxy target' });
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


// SSE proxy — streams events from Go backend with keepalive pings
app.get('/events', (req, res) => {
  try {
    let base = (API_TARGET || '').trim();
    if (!base.startsWith('http://') && !base.startsWith('https://')) {
      base = 'http://' + base;
    }
    const targetUrl = new URL('/api/events', base).toString();
    const proxyReq = http.get(targetUrl, (proxyRes) => {
      res.writeHead(proxyRes.statusCode, {
        'Content-Type': 'text/event-stream',
        'Cache-Control': 'no-cache',
        'Connection': 'keep-alive',
        'Access-Control-Allow-Origin': '*',
      });
      // send keepalive comment every 15s to prevent browser/proxy disconnect
      const keepalive = setInterval(() => {
        res.write(':keepalive\n\n');
      }, 15000);
      proxyRes.on('data', (chunk) => res.write(chunk));
      proxyRes.on('end', () => { clearInterval(keepalive); res.end(); });
      proxyRes.on('error', () => { clearInterval(keepalive); res.end(); });
      req.on('close', () => { clearInterval(keepalive); proxyReq.destroy(); });
    });
    proxyReq.on('error', (err) => {
      console.error('SSE proxy error:', err);
      if (!res.headersSent) res.status(502).json({ error: 'Backend unreachable' });
      else res.end();
    });
  } catch (err) {
    console.error('SSE proxy build error:', err);
    if (!res.headersSent) res.status(502).json({ error: 'Invalid proxy target' });
    else res.end();
  }
});

app.get('/', (req, res) => {
  res.sendFile(path.join(__dirname, 'public', 'index.html'));
});

if (require.main === module) {
  app.listen(PORT, () => {
    console.log(`Dashboard running at http://localhost:${PORT}`);
    console.log(`Proxying /api/* to ${API_TARGET}`);
  });
}

module.exports = app;
