import { spawn } from 'node:child_process';
import { Readable, Writable } from 'node:stream';
import { pathToFileURL } from 'node:url';

const [sdkEntry, fixturePath, privateHome] = process.argv.slice(2);
if (!sdkEntry || !fixturePath || !privateHome) throw new Error('usage: client.mjs SDK_ENTRY FIXTURE PRIVATE_HOME');
const acp = await import(pathToFileURL(sdkEntry).href);
const child = spawn(fixturePath, [], {
  stdio: ['pipe', 'pipe', 'inherit'],
  env: { ...process.env, HOME: privateHome, PK_HOME: `${privateHome}/.pk` },
});
const stream = acp.ndJsonStream(Writable.toWeb(child.stdin), Readable.toWeb(child.stdout));
try {
  const updates = await acp.client({ name: 'pk-sdk-smoke' }).connectWith(stream, async (ctx) => {
    await ctx.request('initialize', {
      protocolVersion: 1,
      clientInfo: { name: 'pk-sdk-smoke', version: '1.5.0' },
    });
    const session = await ctx.buildSession(process.cwd()).start();
    const prompt = session.prompt('hello');
    const received = [];
    for (;;) {
      const item = await session.nextUpdate();
      if (item.kind === 'stop') {
        if (item.stopReason !== 'end_turn') throw new Error(`unexpected stop reason: ${item.stopReason}`);
        break;
      }
      received.push(item.update.sessionUpdate);
      if (item.update.sessionUpdate === 'agent_message_chunk' && item.update.content.text !== 'SDK fixture reply') {
        throw new Error('assistant update content did not match fixture');
      }
    }
    await prompt;
    session.dispose();
    return received;
  });
  if (!updates.includes('user_message_chunk') || !updates.includes('agent_message_chunk')) {
    throw new Error(`missing expected session updates: ${JSON.stringify(updates)}`);
  }
  console.log(`official ACP SDK exchange passed: ${updates.join(', ')}, end_turn`);
} finally {
  // The fixture serves one connection and intentionally has no shutdown RPC.
  child.kill('SIGTERM');
  await new Promise((resolve) => child.once('exit', resolve));
}
