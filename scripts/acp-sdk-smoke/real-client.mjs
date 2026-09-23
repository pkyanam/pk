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
const children = new Set();
const watchdog = setTimeout(() => {
  for (const process of children) process.kill("SIGKILL");
  console.error("ACP real CLI smoke timed out after 45 seconds");
  process.exit(1);
}, 45_000);
const startAgent = () => {
  const process = spawn(binary, ['acp', '--provider', 'fixture'], { stdio: ['pipe', 'pipe', 'inherit'], env, cwd: privateHome });
  children.add(process);
  process.once('exit', () => children.delete(process));
  const stream = acp.ndJsonStream(Writable.toWeb(process.stdin), Readable.toWeb(process.stdout));
  return { process, stream };
};
const stopAgent = async (process) => {
  if (process.exitCode !== null || process.signalCode !== null) return;
  const exited = new Promise((resolve) => process.once('exit', resolve));
  process.stdin.end();
  const termTimer = setTimeout(() => process.kill('SIGTERM'), 1_000);
  const killTimer = setTimeout(() => process.kill('SIGKILL'), 2_000);
  await exited;
  clearTimeout(termTimer);
  clearTimeout(killTimer);
};
const promptAndReadReply = async (session, text) => {
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
};
try {
  const configured = spawnSync(binary, ['provider', 'add', '--id', 'fixture', '--protocol', 'chat_completions', '--base-url', `http://127.0.0.1:${provider.address().port}/v1`, '--model', 'fixture-model'], { env, encoding: 'utf8', timeout: 10_000 });
  if (configured.status !== 0) throw new Error(`local provider setup failed: ${configured.stderr}`);
  let sessionId;
  {
    const { process, stream } = startAgent();
    await acp.client({ name: 'pk-real-cli-smoke' }).connectWith(stream, async (ctx) => {
      await ctx.request('initialize', { protocolVersion: 1, clientInfo: { name: 'pk-real-cli-smoke', version: '1.5.0' } });
      const session = await ctx.buildSession(privateHome).start();
      sessionId = session.sessionId;
      await promptAndReadReply(session, 'first local turn');
      await promptAndReadReply(session, 'second local turn');
      session.dispose();
    });
    await stopAgent(process);
  }

  // Load the saved session in a fresh process with the same private PK_HOME.
  // The SDK notification handler observes the server's durable-history replay.
  const replayedUsers = [];
  const replayedAssistant = [];
  const resumedAssistant = [];
  const { process, stream } = startAgent();
  await acp.client({ name: 'pk-real-cli-smoke' })
    .onNotification(acp.methods.client.session.update, ({ params }) => {
      if (params.sessionId !== sessionId || !params.update.content || params.update.content.type !== 'text') return;
      if (params.update.sessionUpdate === 'user_message_chunk') replayedUsers.push(params.update.content.text);
      if (params.update.sessionUpdate === 'agent_message_chunk') {
        replayedAssistant.push(params.update.content.text);
        resumedAssistant.push(params.update.content.text);
      }
    })
    .connectWith(stream, async (ctx) => {
      const initialized = await ctx.request('initialize', { protocolVersion: 1, clientInfo: { name: 'pk-real-cli-smoke', version: '1.5.0' } });
      if (initialized.agentCapabilities?.loadSession !== true) throw new Error('agent does not advertise durable session loading');
      await ctx.request(acp.methods.agent.session.load, { sessionId, cwd: privateHome, mcpServers: [] });
      if (!replayedUsers.join('\n').includes('first local turn') || !replayedUsers.join('\n').includes('second local turn')) {
        throw new Error('session/load did not replay saved user history');
      }
      if (replayedAssistant.length < 2 || !replayedAssistant.join('').includes('SDK real CLI reply')) throw new Error('session/load did not replay saved assistant history');
      const result = await ctx.request(acp.methods.agent.session.prompt, {
        sessionId,
        prompt: [{ type: 'text', text: 'third local turn after process restart' }],
      });
      if (result.stopReason !== 'end_turn') throw new Error(`unexpected third-turn stop: ${result.stopReason}`);
      if (resumedAssistant.length !== 3 || resumedAssistant.at(-1) !== 'SDK real CLI reply') throw new Error('third turn did not emit its assistant reply');
    });
  await stopAgent(process);
  if (requests.length !== 3 || requests.some((r) => r.model !== 'fixture-model' || !r.tools?.length)) throw new Error('configured provider/model/tool routing mismatch');
  if (!JSON.stringify(requests[1].messages).includes('first local turn')) throw new Error('second turn lost session context');
  const thirdMessages = JSON.stringify(requests[2].messages);
  if (!thirdMessages.includes('first local turn') || !thirdMessages.includes('second local turn') || !thirdMessages.includes('third local turn after process restart')) {
    throw new Error('third turn after restart lost durable conversation history');
  }
  console.log('official ACP SDK real CLI passed: configured provider, restart/load replay, three turns, retained model and history');
} finally {
  await Promise.all([...children].map(stopAgent));
  provider.closeAllConnections();
  await new Promise((resolve) => provider.close(resolve));
  clearTimeout(watchdog);
}
