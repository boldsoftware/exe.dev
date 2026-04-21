// Live agent runner: POSTs to /api/run (via GET, SSE), streams step events.
let currentES = null;

function el(id) { return document.getElementById(id); }

function fmtCost(v) { return '$' + (v || 0).toFixed(4); }

function appendStep(step) {
  const box = el('run-steps');
  if (!box) return;
  const div = document.createElement('div');
  div.className = 'step step-' + step.kind + (step.error ? ' err' : '');
  const head = document.createElement('div');
  head.className = 'step-head';
  let label = step.kind;
  if (step.name) label += ' – ' + step.name;
  if (step.kind === 'usage') label += ' – ' + (step.input_tokens||0) + ' in / ' + (step.output_tokens||0) + ' out · ' + fmtCost(step.cost_usd);
  head.textContent = label;
  div.appendChild(head);
  if (step.input) {
    const pre = document.createElement('pre');
    pre.textContent = step.input;
    div.appendChild(pre);
  }
  if (step.text) {
    const pre = document.createElement('pre');
    pre.textContent = step.text.length > 4000 ? step.text.slice(0, 4000) + '\n…' : step.text;
    div.appendChild(pre);
  }
  box.appendChild(div);
}

function startRun(event, convID) {
  event.preventDefault();
  const form = event.target;
  const convInput = form.querySelector('[name=conversation]');
  const promptInput = form.querySelector('[name=prompt]');
  const conv = convID || (convInput ? convInput.value.trim() : '');
  const prompt = (promptInput && promptInput.value.trim()) || '';
  const params = new URLSearchParams();
  if (conv) params.set('conversation', conv);
  if (prompt) params.set('prompt', prompt);

  if (currentES) currentES.close();
  const out = el('run-output');
  if (out) out.classList.remove('hidden');
  const steps = el('run-steps');
  if (steps) steps.innerHTML = '';
  const final = el('run-final');
  if (final) final.innerHTML = '';
  el('run-cost').textContent = '$0.0000';
  el('run-tokens').textContent = '0 in / 0 out tokens';
  const status = el('run-status');
  if (status) status.textContent = 'running…';
  const btn = form.querySelector('button');
  if (btn) btn.disabled = true;

  let inTok = 0, outTok = 0, cost = 0;
  const es = new EventSource('/api/run?' + params.toString());
  currentES = es;
  es.onmessage = (e) => {
    let ev;
    try { ev = JSON.parse(e.data); } catch (_) { return; }
    if (ev.step) {
      if (ev.step.kind === 'usage') {
        inTok += ev.step.input_tokens || 0;
        outTok += ev.step.output_tokens || 0;
        cost += ev.step.cost_usd || 0;
        el('run-cost').textContent = fmtCost(cost);
        el('run-tokens').textContent = inTok + ' in / ' + outTok + ' out tokens';
      }
      appendStep(ev.step);
      window.scrollTo(0, document.body.scrollHeight);
    }
    if (ev.done) {
      el('run-cost').textContent = fmtCost(ev.done.cost_usd);
      el('run-tokens').textContent = (ev.done.input_tokens||0) + ' in / ' + (ev.done.output_tokens||0) + ' out tokens';
      if (final && ev.done.output) {
        const h = document.createElement('h3');
        h.textContent = 'Result';
        const pre = document.createElement('pre');
        pre.textContent = ev.done.output;
        final.innerHTML = '';
        final.appendChild(h);
        final.appendChild(pre);
        if (ev.done.result_id) {
          const a = document.createElement('a');
          a.href = '/result/' + ev.done.result_id;
          a.textContent = 'permalink #' + ev.done.result_id;
          final.appendChild(a);
        }
      }
    }
    if (ev.error) {
      const err = document.createElement('div');
      err.className = 'step step-tool_result err';
      err.textContent = 'ERROR: ' + ev.error;
      steps.appendChild(err);
    }
  };
  es.addEventListener('end', () => {
    es.close();
    currentES = null;
    if (btn) btn.disabled = false;
    if (status) status.textContent = 'done';
  });
  es.onerror = () => {
    es.close();
    currentES = null;
    if (btn) btn.disabled = false;
    if (status) status.textContent = 'connection closed';
  };
  return false;
}
