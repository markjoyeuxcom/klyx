// fake-model.mjs - the AI eval's scripted OpenAI-compatible model (eval
// design §9). It serves loopback only and never leaves the machine. Each
// request's model name picks a script, so one eval run per script tests the
// harness end to end with no key:
//
//   eval-grounded           E1: reads the unit, the Git changes and the previous log, and cites them;
//                           E3 and E4: reads the anchor's events and cites them;
//                           any other anchor: an explicit empty report
//   eval-lookalike          E3 and E4: reads a lookalike and cites only it
//   eval-negated            E1's reads, then a cause that negates the planted one
//   eval-at-once            reports a cause at once, citing only the anchor's read
//   eval-current-log        reads only a current container's log, and cites it
//   eval-no-report          answers without ever calling report_findings
//   eval-empty-then-fail    an empty report, then the provider fails
//   eval-unsupported        a cause citing a read that does not exist
//   eval-no-cause-proven    an observation saying no cause is proven
//   eval-drift              like eval-at-once, after waiting 20 s (the self-test changes a witness meanwhile)
//   eval-snapshot-block     refuses tools; the snapshot answer carries a valid block
//   eval-snapshot-none      refuses tools; the snapshot answer carries no block
//   eval-snapshot-malformed refuses tools; the snapshot answer's block does not parse
//
// usage: node fake-model.mjs [port]   (default 18791)
import http from 'node:http';

const port = Number(process.argv[2] ?? 18791);
const NS_FLUX = 'klyx-a1-flux', NS_APP = 'klyx-a1-app', UNIT = 'a1-web';

const results = (msgs) => msgs.filter((m) => m.role === 'tool').map((m) => { try { return JSON.parse(m.content); } catch { return {}; } });
const byTool = (msgs, tool) => results(msgs).filter((r) => r.tool === tool);
const checksOf = (env, cls) => (env?.checks ?? []).filter((c) => !cls || c.class === cls);
let n = 0;
const call = (name, args) => ({ id: `call_${++n}`, type: 'function', function: { name, arguments: JSON.stringify(args) } });
const report = (findings, extra = {}) => ({ calls: [call('report_findings', { findings, ...extra })] });
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

// anchor reads the seeded object's kind and name from the first message.
function anchor(msgs) {
  const user = msgs.find((m) => m.role === 'user')?.content ?? '';
  const m = user.match(/<seeded-object>\n([\s\S]*?)\n<\/seeded-object>/);
  try {
    const env = JSON.parse(m?.[1] ?? '{}');
    const o = env.data?.object ?? {};
    return { kind: o.kind ?? '', namespace: o.namespace ?? '', name: o.name ?? '', readId: env.readId ?? 'r1' };
  } catch {
    return { kind: '', namespace: '', name: '', readId: 'r1' };
  }
}

const isE1 = (a) => a.kind === 'Kustomization' && a.namespace === NS_FLUX && a.name === UNIT;
const isE3 = (a) => a.kind === 'ScaledObject' && a.namespace === 'klyx-eval-e3' && a.name === 'no-target';
const isE4 = (a) => a.kind === 'Service' && a.namespace === 'klyx-eval-e4' && a.name === 'one-b';
const lookalikeOf = { 'klyx-eval-e3': { kind: 'ScaledObject', name: 'bad-auth' }, 'klyx-eval-e4': { kind: 'Service', name: 'cilium-l2' } };

// eventsCause is E3's and E4's grounded run: the anchor's events, then a
// cause about the anchor citing them.
function eventsCause(a, msgs, turns, statement) {
  if (turns === 0) return { calls: [call('get_events', { namespace: a.namespace, kind: a.kind, name: a.name, limit: 20 })] };
  const evs = byTool(msgs, 'get_events')[0];
  return report([{ type: 'cause', statement, subject: { kind: a.kind, namespace: a.namespace, name: a.name }, cites: [evs?.readId ?? a.readId] }]);
}
const deployment = { kind: 'Deployment', namespace: NS_APP, name: 'web' };

// e1Reads is E1's investigation: the unit, the Git changes, the
// dependencies, then the previous log of a web pod. It returns the next
// calls, or null once everything is read.
function e1Reads(msgs, turns) {
  switch (turns) {
    case 0:
      return [call('gitops_unit', { kind: 'Kustomization', namespace: NS_FLUX, name: UNIT })];
    case 1:
      return [call('git_changes', { kind: 'Kustomization', namespace: NS_FLUX, name: UNIT }), call('get_dependencies', { kind: 'Deployment', namespace: NS_APP, name: 'web' })];
    case 2: {
      const deps = byTool(msgs, 'get_dependencies')[0];
      const pods = (deps?.data?.nodes ?? []).filter((x) => x.kind === 'Pod' && x.uid);
      if (pods.length) return pods.slice(0, 2).map((p) => call('get_pod_logs', { namespace: NS_APP, pod: p.name, previous: true, tail: 20 }));
      return null;
    }
    default:
      return null;
  }
}

function plantedCites(msgs) {
  const changes = byTool(msgs, 'git_changes')[0];
  const diff = checksOf(changes, 'diff').find((c) => c.subject?.kind === 'Deployment');
  const logs = byTool(msgs, 'get_pod_logs').filter((l) => l.data?.instance === 'previous' && l.data?.lines > 0);
  return [diff?.id ?? changes?.readId, ...logs.map((l) => l.readId)].filter(Boolean);
}

async function tools(script, msgs) {
  const a = anchor(msgs);
  const turns = msgs.filter((m) => m.role === 'assistant').length;
  const reported = msgs.some((m) => (m.tool_calls ?? []).some((c) => c.function?.name === 'report_findings'));
  if (reported) {
    if (script === 'eval-empty-then-fail') return { fail: 500 };
    return { content: 'The findings above cite the reads they rest on.' };
  }
  switch (script) {
    case 'eval-lookalike': {
      const l = lookalikeOf[a.namespace];
      if (!l) return report([]);
      if (turns === 0) return { calls: [call('get_resource', { kind: l.kind, namespace: a.namespace, name: l.name })] };
      const got = byTool(msgs, 'get_resource')[0];
      return report([{ type: 'cause', statement: `The failure comes from ${l.name}'s configuration.`, subject: { kind: a.kind, namespace: a.namespace, name: a.name }, cites: [got?.readId ?? a.readId] }]);
    }
    case 'eval-grounded':
    case 'eval-negated': {
      if (script === 'eval-grounded' && isE3(a)) return eventsCause(a, msgs, turns, 'The ScaledObject targets a Deployment, no-such-deployment, that does not exist.');
      if (script === 'eval-grounded' && isE4(a)) return eventsCause(a, msgs, turns, "The pool's one address is held by one-a, so MetalLB has no available IPs for one-b.");
      if (!isE1(a)) return report([]);
      const next = e1Reads(msgs, turns);
      if (next) return { calls: next };
      const statement = script === 'eval-negated'
        ? 'MODE was correct; a registry outage caused the crash.'
        : "The second revision changed web's MODE environment value; the new pods exit as they start, so the rollout never completes.";
      return report([{ type: 'cause', statement, subject: deployment, cites: plantedCites(msgs) }]);
    }
    case 'eval-at-once':
      return report([{ type: 'cause', statement: 'The workload is misconfigured.', subject: isE1(a) ? deployment : { kind: a.kind, namespace: a.namespace, name: a.name }, cites: [a.readId] }]);
    case 'eval-drift':
      await sleep(20000);
      return report([]);
    case 'eval-current-log': {
      if (turns === 0) return { calls: [call('get_dependencies', { kind: 'Deployment', namespace: NS_APP, name: 'web' })] };
      if (turns === 1) {
        const pod = (byTool(msgs, 'get_dependencies')[0]?.data?.nodes ?? []).find((x) => x.kind === 'Pod' && x.uid);
        if (pod) return { calls: [call('get_pod_logs', { namespace: NS_APP, pod: pod.name, previous: false, tail: 20 })] };
      }
      const logs = byTool(msgs, 'get_pod_logs')[0];
      return report([{ type: 'cause', statement: 'The current container logs show the problem.', subject: deployment, cites: [logs?.readId ?? a.readId] }]);
    }
    case 'eval-no-report':
      return { content: 'A registry outage caused the failure.' };
    case 'eval-empty-then-fail':
      return report([]);
    case 'eval-unsupported':
      return report([{ type: 'cause', statement: 'A DNS outage in the cluster caused it.', subject: { kind: a.kind, namespace: a.namespace, name: a.name }, cites: ['r99'] }]);
    case 'eval-no-cause-proven':
      return report([{ type: 'observation', statement: 'No cause is proven; the object reads as healthy.', subject: { kind: a.kind, namespace: a.namespace, name: a.name }, cites: [a.readId] }]);
    default:
      return { content: `unknown script ${script}` };
  }
}

function snapshot(script, body) {
  const doc = body.messages?.find((m) => m.role === 'user')?.content ?? '';
  const first = doc.match(/"readId":"(r\d+)"/)?.[1] ?? 'r1';
  switch (script) {
    case 'eval-snapshot-block':
      return { content: `The object was read.\n\n\`\`\`findings\n{"findings":[{"type":"observation","statement":"the anchor was read","subject":{"kind":"Kustomization","namespace":"${NS_FLUX}","name":"${UNIT}"},"cites":["${first}"]}]}\n\`\`\`` };
    case 'eval-snapshot-malformed':
      return { content: 'The object was read.\n\n```findings\n{"findings": [unfinished\n```' };
    default:
      return { content: 'A registry outage caused the failure.' };
  }
}

http.createServer((req, res) => {
  let raw = '';
  req.on('data', (c) => (raw += c));
  req.on('end', async () => {
    if (req.method === 'GET' && req.url === '/v1/models') {
      res.writeHead(200, { 'content-type': 'application/json' });
      res.end(JSON.stringify({ object: 'list', data: [{ id: 'eval-grounded', object: 'model' }] }));
      return;
    }
    if (req.method !== 'POST' || req.url !== '/v1/chat/completions') {
      res.writeHead(404); res.end(); return;
    }
    let body = {};
    try { body = JSON.parse(raw); } catch { res.writeHead(400); res.end(); return; }
    const script = body.model ?? 'eval-grounded';
    const hasTools = (body.tools ?? []).length > 0;
    if (hasTools && script.startsWith('eval-snapshot')) {
      console.log(new Date().toISOString(), script, 'refused tools');
      res.writeHead(400, { 'content-type': 'application/json' });
      res.end(JSON.stringify({ error: { message: 'this model does not support tools' } }));
      return;
    }
    if (script === 'eval-drift' && hasTools) console.log(new Date().toISOString(), script, 'drift: waiting');
    const s = hasTools ? await tools(script, body.messages ?? []) : snapshot(script, body);
    if (s.fail) {
      console.log(new Date().toISOString(), script, `fail ${s.fail}`);
      res.writeHead(s.fail, { 'content-type': 'application/json' });
      res.end(JSON.stringify({ error: { message: 'scripted provider failure' } }));
      return;
    }
    const message = s.calls ? { role: 'assistant', content: '', tool_calls: s.calls } : { role: 'assistant', content: s.content };
    const out = { id: `chatcmpl-${Date.now()}`, object: 'chat.completion', model: script,
      choices: [{ index: 0, finish_reason: s.calls ? 'tool_calls' : 'stop', message }], usage: { prompt_tokens: 0, completion_tokens: 0, total_tokens: 0 } };
    console.log(new Date().toISOString(), script, s.calls ? `calls ${s.calls.map((c) => c.function.name).join(',')}` : 'text');
    res.writeHead(200, { 'content-type': 'application/json' });
    res.end(JSON.stringify(out));
  });
}).listen(port, '127.0.0.1', () => console.log(`eval model on 127.0.0.1:${port}`));
