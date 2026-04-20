const sessionId = process.argv[2];
const message = process.argv[3] || '[A3C] Test broadcast message';

const data = JSON.stringify({
  parts: [{ type: 'text', text: message }]
});

const req = require('http').request({
  hostname: '127.0.0.1',
  port: 4096,
  path: `/session/${sessionId}/message`,
  method: 'POST',
  headers: {
    'Content-Type': 'application/json',
    'Content-Length': Buffer.byteLength(data)
  },
  timeout: 5000
}, (res) => {
  let body = '';
  res.on('data', (chunk) => body += chunk);
  res.on('end', () => {
    console.log('Status:', res.statusCode);
    if (body.length < 500) console.log('Body:', body);
    process.exit(0);
  });
});

req.on('error', (e) => {
  console.error('Error:', e.message);
  process.exit(1);
});

req.on('timeout', () => {
  console.log('Request sent (timeout is expected for long-running responses)');
  req.destroy();
  process.exit(0);
});

req.write(data);
req.end();