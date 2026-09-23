// Exercise the real CLI/runner/provider path, with no remote model or credentials.
import { spawn, spawnSync } from 'node:child_process';
import { createServer } from 'node:http';
import { Readable, Writable } from 'node:stream';
import { pathToFileURL } from 'node:url';

const [sdkEntry, binary, privateHome] = process.argv.slice(2);
const acp = await import(pathToFileURL(sdkEntry).href);
const requests = [];
const provider = createServer(async (req, res) => {
  if (req.url !== '/v1/chat/completions') { res.writeHead(404).end(); return; }
  let body = '';
  for await (const chunk of req) body += chunk;
  requests.push(JSON.parse(body));
  res.writeHead(200, { 'Content-Type': 'text/event-stream' });
  res.end(`data: ${JSON.stringify({ id: `acp-fixture-${requests.length}`, choices: [{ index: 0, delta: { content: 'SDK real CLI reply' }, finish_reason: 'stop' }], usage: { prompt_tokens: 10, completion_tokens: 4 } })}\n\ndata: [DONE]\n\n`);
});
await new Promise((resolve) => provider.listen(0, '127.0.0.1', resolve));
const env = Object.fromEntries(Object.entries(process.env).filter(([key]) => !key.startsWith('PK_')));
Object.assign(env, { HOME: privateHome, PK_HOME: `${privateHome}/.pk` });
let child;
const watchdog = setTimeout(() => {
  child?.kill("SIGKILL");
  console.error("ACP real CLI smoke timed out after 45 seconds");
  process.exit(1);
}, 45_000);
try {
  const configured = spawnSync(binary, ['provider', 'add', '--id', 'fixture', '--protocol', 'chat_completions', '--base-url', `http://127.0.0.1:${provider.address().port}/v1`, '--model', 'fixture-model'], { env, encoding: 'utf8', timeout: 10_000 });
  if (configured.status !== 0) throw new Error(`local provider setup failed: ${configured.stderr}`);
  child = spawn(binary, ['acp', '--provider', 'fixture'], { stdio: ['pipe', 'pipe', 'inherit'], env, cwd: privateHome });
  const stream = acp.ndJsonStream(Writable.toWeb(child.stdin), Readable.toWeb(child.stdout));
  await acp.client({ name: 'pk-real-cli-smoke' }).connectWith(stream, async (ctx) => {
    await ctx.request('initialize', { protocolVersion: 1, clientInfo: { name: 'pk-real-cli-smoke', version: '1.5.0' } });
    const session = await ctx.buildSession(privateHome).start();
    for (const text of ['first local turn', 'second local turn']) {
      const prompt = session.prompt(text);
      let assistant = '';
      for (;;) {
        const item = await session.nextUpdate();
        if (item.kind === 'stop') {
          if (item.stopReason !== 'end_turn') throw new Error(`unexpected stop: ${item.stopReason}`);
          break;
        }
        if (item.update.sessionUpdate === 'agent_message_chunk') assistant += item.update.content.text;
      }
      await prompt;
      if (assistant !== 'SDK real CLI reply') throw new Error('missing real runner assistant output');
    }
    session.dispose();
  });
  if (requests.length !== 2 || requests.some((r) => r.model !== 'fixture-model' || !r.tools?.length)) throw new Error('configured provider/model/tool routing mismatch');
  if (!JSON.stringify(requests[1].messages).includes('first local turn')) throw new Error('second turn lost session context');
  console.log('official ACP SDK real CLI passed: configured Chat Completions provider, two turns, retained context');
} finally {
  if (child && child.exitCode === null) {
    const exited = new Promise((resolve) => child.once('exit', resolve));
    child.kill('SIGTERM');
    await exited;
  }
  provider.closeAllConnections();
  await new Promise((resolve) => provider.close(resolve));
  clearTimeout(watchdog);
}
