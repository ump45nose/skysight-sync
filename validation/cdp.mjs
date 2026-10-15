// Validation-only helper. Reuses an already running client's loopback CDP port.
import { readFile, writeFile } from 'node:fs/promises';

const [port, targetId = 'main', action = 'eval', outputPath] = process.argv.slice(2);
const targets = await (await fetch(`http://127.0.0.1:${port}/json/list`)).json();
const target = targetId === 'main'
  ? targets.find(t => t.type === 'page' && t.url === 'app://-/index.html')
  : targets.find(t => t.id === targetId);
if (!target) throw new Error('Requested app target not found');

const socket = new WebSocket(target.webSocketDebuggerUrl);
const pending = new Map();
let sequence = 0;
socket.onmessage = ({ data }) => {
  const message = JSON.parse(data);
  const request = pending.get(message.id);
  if (!request) return;
  pending.delete(message.id);
  clearTimeout(request.timeout);
  message.error ? request.reject(new Error(JSON.stringify(message.error))) : request.resolve(message.result);
};
await new Promise((resolve, reject) => { socket.onopen = resolve; socket.onerror = reject; });

function call(method, params) {
  return new Promise((resolve, reject) => {
    const id = ++sequence;
    const timeout = setTimeout(() => { pending.delete(id); reject(new Error(`${method} timed out`)); }, 20000);
    pending.set(id, { resolve, reject, timeout });
    socket.send(JSON.stringify({ id, method, params }));
  });
}

try {
  if (action === 'screenshot') {
    if (!outputPath) throw new Error('Screenshot needs an absolute output path');
    const result = await call('Page.captureScreenshot', { format: 'png' });
    await writeFile(outputPath, Buffer.from(result.data, 'base64'));
    console.log(JSON.stringify({ path: outputPath }));
  } else {
    const expression = await readFile('/dev/stdin', 'utf8');
    const result = await call('Runtime.evaluate', { expression, returnByValue: true, awaitPromise: true });
    if (result.exceptionDetails) throw new Error(JSON.stringify(result.exceptionDetails));
    console.log(JSON.stringify(result.result.value ?? result.result));
  }
} finally {
  socket.close();
}
