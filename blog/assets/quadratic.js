// Shared data processing for token visualizations
async function loadTokenData(csvUrl) {
  const PRICE_INPUT = 5.0;
  const PRICE_CACHE_WRITE = 6.25;
  const PRICE_OUTPUT = 25.0;
  const PRICE_CACHE_READ = 0.50;

  const response = await fetch(csvUrl);
  const text = await response.text();
  const lines = text.trim().split('\n').filter(l => l.trim());
  const firstLine = lines[0].toLowerCase();
  const hasHeader = firstLine.includes('input') || firstLine.includes('token');
  const dataLines = hasHeader ? lines.slice(1) : lines;

  const messages = dataLines.map(line => {
    const parts = line.split(',').map(s => parseInt(s.trim(), 10) || 0);
    return {
      input_tokens: parts[0],
      cache_write_tokens: parts[1],
      cache_read_tokens: parts[2],
      output_tokens: parts[3]
    };
  });

  const tokenRecords = [];
  const costRecords = [];
  let cumulativeInputContext = 0;
  let cacheReadLayer = 0;
  let cumulativeTotalCost = 0;
  let cumulativeCacheReadCost = 0;

  for (let msgIdx = 0; msgIdx < messages.length; msgIdx++) {
    const msg = messages[msgIdx];
    const inputTokens = msg.input_tokens;
    const cacheWrite = msg.cache_write_tokens;
    const cacheRead = msg.cache_read_tokens;
    const outputTokens = msg.output_tokens;

    const msgInputCost = inputTokens * PRICE_INPUT / 1_000_000;
    const msgCacheWriteCost = cacheWrite * PRICE_CACHE_WRITE / 1_000_000;
    const msgCacheReadCost = cacheRead * PRICE_CACHE_READ / 1_000_000;
    const msgOutputCost = outputTokens * PRICE_OUTPUT / 1_000_000;

    cumulativeTotalCost += msgInputCost + msgCacheWriteCost + msgCacheReadCost + msgOutputCost;
    cumulativeCacheReadCost += msgCacheReadCost;

    if (cacheRead > 0) {
      tokenRecords.push({
        msg_idx: msgIdx, type: 'cache_read',
        x: 0, x2: cacheRead, width: cacheRead,
        price: PRICE_CACHE_READ, row: 'cache_read', y_offset: cacheReadLayer
      });
      cacheReadLayer++;
    }

    const xInputStart = cumulativeInputContext;

    if (inputTokens > 0) {
      tokenRecords.push({
        msg_idx: msgIdx, type: 'input',
        x: xInputStart, x2: xInputStart + inputTokens, width: inputTokens,
        price: PRICE_INPUT, row: 'input', y_offset: 0
      });
    }

    if (cacheWrite > 0) {
      tokenRecords.push({
        msg_idx: msgIdx, type: 'cache_write',
        x: xInputStart + inputTokens, x2: xInputStart + inputTokens + cacheWrite,
        width: cacheWrite, price: PRICE_CACHE_WRITE, row: 'input', y_offset: 0
      });
    }

    if (outputTokens > 0) {
      tokenRecords.push({
        msg_idx: msgIdx, type: 'output',
        x: xInputStart + inputTokens + cacheWrite,
        x2: xInputStart + inputTokens + cacheWrite + outputTokens,
        width: outputTokens, price: PRICE_OUTPUT, row: 'output', y_offset: 0
      });
    }

    const xEnd = xInputStart + inputTokens + cacheWrite + outputTokens;
    const pctCacheRead = cumulativeTotalCost > 0 ? (cumulativeCacheReadCost / cumulativeTotalCost * 100) : 0;

    costRecords.push({ x: xEnd, value: cumulativeTotalCost, metric: 'Total Cost', msg_idx: msgIdx });
    costRecords.push({ x: xEnd, value: cumulativeCacheReadCost, metric: 'Cache Read Cost', msg_idx: msgIdx });
    costRecords.push({ x: xEnd, pct: pctCacheRead, metric: '% Cache Read', msg_idx: msgIdx });

    cumulativeInputContext += inputTokens + cacheWrite;
  }

  const maxX = Math.max(...tokenRecords.map(r => r.x2), ...costRecords.map(r => r.x)) * 1.05;

  return { tokenRecords, costRecords, maxX };
}

// Renders the token rectangles visualization
async function renderTokenRectangles(csvUrl, selector) {
  const { tokenRecords, maxX } = await loadTokenData(csvUrl);

  // Calculate the y extent for cache reads to position labels
  const maxCacheReadLayer = Math.max(...tokenRecords.filter(r => r.row === 'cache_read').map(r => r.y_offset), 0);
  const cacheReadBottom = -2 - (maxCacheReadLayer * 0.5) - 0.5;

  const labels = [
    {"label": "Outputs", "y": 24},
    {"label": "Input + Cache Write", "y": 3},
    {"label": "Cache Reads", "y": (cacheReadBottom - 2) / 2}
  ];

  const spec = {
    "$schema": "https://vega.github.io/schema/vega-lite/v5.json",
    "width": 700, "height": 200,
    "params": [{"name": "cutoff", "value": maxX}],
    "layer": [
      {
        "data": {"values": tokenRecords},
        "transform": [
          {"calculate": "datum.row === 'output' ? 12 : (datum.row === 'input' ? 0 : -2 - (datum.y_offset * 0.5))", "as": "y"},
          {"calculate": "datum.row === 'output' ? 12 + datum.price : (datum.row === 'input' ? datum.price : -2 - (datum.y_offset * 0.5) - datum.price)", "as": "y2"}
        ],
        "mark": {"type": "rect"},
        "encoding": {
          "x": {"field": "x", "type": "quantitative", "axis": null, "scale": {"domain": [0, maxX]}},
          "x2": {"field": "x2"},
          "y": {"field": "y", "type": "quantitative", "axis": null},
          "y2": {"field": "y2"},
          "color": {"field": "msg_idx", "type": "quantitative", "scale": {"scheme": "viridis"}, "legend": null},
          "opacity": {"condition": {"test": "datum.x2 <= cutoff", "value": 1}, "value": 0.15},
          "tooltip": [
            {"field": "msg_idx", "title": "Message"},
            {"field": "type", "title": "Type"},
            {"field": "width", "title": "Tokens"},
            {"field": "price", "title": "$/M tokens"}
          ]
        }
      },
      {
        "data": {"values": labels},
        "mark": {"type": "text", "align": "left", "fontSize": 11, "fill": "#666"},
        "encoding": {
          "x": {"value": 710},
          "y": {"field": "y", "type": "quantitative"},
          "text": {"field": "label"}
        }
      }
    ]
  };

  const result = await vegaEmbed(selector, spec, {actions: false});
  const view = result.view;

  // Animation state
  let animating = true;
  let animationFrame;
  const duration = 8000; // 8 seconds for a full cycle
  const startTime = performance.now();

  function animate(now) {
    if (!animating) return;
    const elapsed = (now - startTime) % (duration * 2);
    // Triangle wave: go forward then backward
    const progress = elapsed < duration
      ? elapsed / duration
      : 2 - elapsed / duration;
    view.signal('cutoff', progress * maxX).run();
    animationFrame = requestAnimationFrame(animate);
  }
  animationFrame = requestAnimationFrame(animate);

  // Get the chart element and track mouse
  const el = document.querySelector(selector);
  const canvas = el.querySelector('canvas');

  canvas.addEventListener('mouseenter', () => {
    animating = false;
    cancelAnimationFrame(animationFrame);
  });

  canvas.addEventListener('mousemove', (e) => {
    const rect = canvas.getBoundingClientRect();
    const x = e.clientX - rect.left;
    // Use Vega's scale to convert pixel to data value
    const xScale = view.scale('x');
    if (xScale) {
      const contextPos = xScale.invert(x);
      view.signal('cutoff', Math.max(0, Math.min(maxX, contextPos))).run();
    }
  });

  canvas.addEventListener('mouseleave', () => {
    // Resume animation
    animating = true;
    animationFrame = requestAnimationFrame(animate);
  });
}

// Renders an interactive cost simulator
function renderCostSimulator(selector) {
  const PRICE_INPUT = 5.0;
  const PRICE_CACHE_WRITE = 6.25;
  const PRICE_OUTPUT = 25.0;
  const PRICE_CACHE_READ = 0.50;

  const container = document.querySelector(selector);
  container.innerHTML = `
    <div class="sim-controls" style="display: grid; grid-template-columns: repeat(auto-fit, minmax(150px, 1fr)); gap: 12px; margin-bottom: 16px;">
      <div style="display: flex; flex-direction: column; gap: 4px;">
        <label style="font-size: 12px; color: #555;">Initial Prompt (cache write)</label>
        <input type="number" id="sim-initialPrompt" value="10000" min="0" step="1000" style="padding: 6px; border: 1px solid #ccc; border-radius: 4px;">
      </div>
      <div style="display: flex; flex-direction: column; gap: 4px;">
        <label style="font-size: 12px; color: #555;">Input per Call (cache write)</label>
        <input type="number" id="sim-inputPerCall" value="250" min="0" step="50" style="padding: 6px; border: 1px solid #ccc; border-radius: 4px;">
      </div>
      <div style="display: flex; flex-direction: column; gap: 4px;">
        <label style="font-size: 12px; color: #555;">Output per Call (tokens)</label>
        <input type="number" id="sim-outputPerCall" value="100" min="0" step="50" style="padding: 6px; border: 1px solid #ccc; border-radius: 4px;">
      </div>
      <div style="display: flex; flex-direction: column; gap: 4px;">
        <label style="font-size: 12px; color: #555;">Final Context Length</label>
        <input type="number" id="sim-finalContext" value="150000" min="1000" step="10000" style="padding: 6px; border: 1px solid #ccc; border-radius: 4px;">
      </div>
    </div>
    <div id="sim-viz"></div>
    <div id="sim-summary" style="margin-top: 12px; font-size: 13px; color: #555;"></div>
  `;

  function simulate() {
    const initialPrompt = parseInt(document.getElementById('sim-initialPrompt').value) || 0;
    const inputPerCall = parseInt(document.getElementById('sim-inputPerCall').value) || 0;
    const outputPerCall = parseInt(document.getElementById('sim-outputPerCall').value) || 0;
    const finalContext = parseInt(document.getElementById('sim-finalContext').value) || 150000;

    // Calculate number of calls needed to reach final context length
    // After call 0: contextSize = initialPrompt + outputPerCall
    // After call n: contextSize = initialPrompt + outputPerCall + n * (inputPerCall + outputPerCall)
    const tokensPerCall = inputPerCall + outputPerCall;
    const firstCallContext = initialPrompt + outputPerCall;
    const numCalls = tokensPerCall > 0
      ? Math.max(1, Math.ceil(1 + (finalContext - firstCallContext) / tokensPerCall))
      : 1;

    const records = [];
    let cumulativeCost = 0;
    let cumulativeCacheReadCost = 0;
    let contextSize = 0;

    // Starting point: just the initial prompt, no cost yet
    records.push({
      call: 0,
      contextSize: initialPrompt,
      totalCost: 0,
      cacheReadCost: 0,
      pctCacheRead: 0
    });

    for (let i = 0; i < numCalls; i++) {
      let cacheWriteTokens, cacheReadTokens, outputTokens;

      if (i === 0) {
        cacheWriteTokens = initialPrompt;
        cacheReadTokens = 0;
        outputTokens = outputPerCall;
      } else {
        cacheWriteTokens = inputPerCall + outputPerCall;
        cacheReadTokens = contextSize;
        outputTokens = outputPerCall;
      }

      const cacheWriteCost = cacheWriteTokens * PRICE_CACHE_WRITE / 1_000_000;
      const cacheReadCost = cacheReadTokens * PRICE_CACHE_READ / 1_000_000;
      const outputCost = outputTokens * PRICE_OUTPUT / 1_000_000;

      cumulativeCost += cacheWriteCost + cacheReadCost + outputCost;
      cumulativeCacheReadCost += cacheReadCost;

      if (i === 0) {
        contextSize = initialPrompt + outputPerCall;
      } else {
        contextSize += inputPerCall + outputPerCall;
      }

      const pctCacheRead = cumulativeCost > 0 ? (cumulativeCacheReadCost / cumulativeCost * 100) : 0;

      records.push({
        call: i + 1,
        contextSize: contextSize,
        totalCost: cumulativeCost,
        cacheReadCost: cumulativeCacheReadCost,
        pctCacheRead: pctCacheRead
      });
    }

    const maxX = Math.max(...records.map(r => r.contextSize)) * 1.05;

    // Transform records for combined cost lines
    const costLines = [];
    for (const r of records) {
      costLines.push({...r, metric: 'Total Cost', value: r.totalCost});
      costLines.push({...r, metric: 'Cache Read Cost', value: r.cacheReadCost});
    }

    const spec = {
      "$schema": "https://vega.github.io/schema/vega-lite/v5.json",
      "width": 700,
      "height": 180,
      "layer": [
        {
          "data": {"values": costLines},
          "mark": {"type": "line", "strokeWidth": 2},
          "encoding": {
            "x": {"field": "contextSize", "type": "quantitative", "title": "Context Size (tokens)", "scale": {"domain": [0, maxX]}},
            "y": {"field": "value", "type": "quantitative", "title": "Cumulative Cost ($)"},
            "color": {"field": "metric", "type": "nominal", "scale": {"domain": ["Total Cost", "Cache Read Cost", "% Cache Read"], "range": ["steelblue", "orange", "green"]}, "legend": {"title": null}}
          }
        },
        {
          "data": {"values": records},
          "mark": {"type": "line", "strokeWidth": 2, "strokeDash": [4, 4]},
          "encoding": {
            "x": {"field": "contextSize", "type": "quantitative"},
            "y": {"field": "pctCacheRead", "type": "quantitative", "title": "% Cache Read", "scale": {"domain": [0, 100]}, "axis": {"orient": "right", "grid": false}},
            "color": {"datum": "% Cache Read"}
          }
        },
        {
          "data": {"values": [{"pctCacheRead": 50}]},
          "mark": {"type": "rule", "strokeDash": [2, 2], "stroke": "#999", "strokeWidth": 1},
          "encoding": {
            "y": {"field": "pctCacheRead", "type": "quantitative", "scale": {"domain": [0, 100]}, "axis": null}
          }
        },
        {
          "data": {"values": records},
          "params": [{
            "name": "hover",
            "select": {"type": "point", "on": "mouseover", "nearest": true, "clear": "mouseout"}
          }],
          "mark": {"type": "rule", "strokeWidth": 1, "stroke": "#999"},
          "encoding": {
            "x": {"field": "contextSize", "type": "quantitative"},
            "opacity": {"condition": {"param": "hover", "empty": false, "value": 0.7}, "value": 0},
            "tooltip": [
              {"field": "call", "title": "Call #"},
              {"field": "contextSize", "title": "Context Size", "format": ","},
              {"field": "totalCost", "title": "Total Cost", "format": "$.2f"},
              {"field": "cacheReadCost", "title": "Cache Read Cost", "format": "$.2f"},
              {"field": "pctCacheRead", "title": "% Cache Read", "format": ".1f"}
            ]
          }
        }
      ],
      "resolve": {"scale": {"y": "independent"}}
    };

    vegaEmbed('#sim-viz', spec, {actions: false});

    const final = records[records.length - 1];
    document.getElementById('sim-summary').innerHTML = `
      ${final.contextSize.toLocaleString()} tokens, ${numCalls} calls, <strong>$${final.totalCost.toFixed(2)}</strong> (cache read $${final.cacheReadCost.toFixed(2)}, ${final.pctCacheRead.toFixed(1)}%)
    `;
  }

  simulate();
  container.querySelectorAll('input').forEach(input => {
    input.addEventListener('input', simulate);
  });
}

// Renders multi-conversation cumulative cost visualization
// Renders cost at 100k tokens scatter plot
async function renderCostAt100k(jsonUrl, selector) {
  const response = await fetch(jsonUrl);
  const data = await response.json();

  const spec = {
    $schema: 'https://vega.github.io/schema/vega-lite/v5.json',
    data: { values: data },
    width: 500,
    height: 300,
    mark: { type: 'circle', size: 100, opacity: 0.7 },
    encoding: {
      x: { field: 'turns', type: 'quantitative', title: 'Number of LLM Calls' },
      y: { field: 'cost', type: 'quantitative', title: 'Cumulative Cost ($)', axis: { format: '$,.0f' } },
      color: {
        field: 'cache_read_pct',
        type: 'quantitative',
        title: 'Cache Read %',
        scale: { scheme: 'viridis' }
      },
      tooltip: [
        { field: 'conversation_id', title: 'Conversation' },
        { field: 'turns', title: 'LLM Calls' },
        { field: 'cost', title: 'Total Cost', format: '$,.2f' },
        { field: 'cache_read_cost', title: 'Cache Read Cost', format: '$,.2f' },
        { field: 'cache_read_pct', title: 'Cache Read %', format: '.1f' },
        { field: 'context_size', title: 'Context Size', format: ',.0f' }
      ]
    }
  };

  vegaEmbed(selector, spec, { actions: false });
}

async function renderConversationCosts(jsonUrl, selector) {
  const response = await fetch(jsonUrl);
  const rawData = await response.json();

  // Fix non-monotonic context: track max context seen per conversation
  const convMaxContext = {};
  const data = rawData.map(d => {
    const cid = d.conversation_id;
    const prevMax = convMaxContext[cid] || 0;
    const newMax = Math.max(prevMax, d.context_size);
    convMaxContext[cid] = newMax;
    return { ...d, monotonic_context: newMax };
  });

  // Find endpoints (last point for each conversation)
  const lastTurn = {};
  data.forEach(d => {
    const cid = d.conversation_id;
    if (!lastTurn[cid] || d.turn > lastTurn[cid]) {
      lastTurn[cid] = d.turn;
    }
  });
  const endpoints = data.filter(d => d.turn === lastTurn[d.conversation_id]);

  const spec = {
    $schema: 'https://vega.github.io/schema/vega-lite/v5.json',
    params: [
      {
        name: 'hover',
        select: {
          type: 'point',
          fields: ['conversation_id'],
          on: 'pointerover',
          clear: 'pointerout'
        }
      }
    ],
    hconcat: [
      {
        width: 340,
        height: 250,
        title: 'Total Cost',
        layer: [
          {
            data: { values: data },
            mark: { type: 'line', strokeWidth: 1.5 },
            encoding: {
              x: { field: 'monotonic_context', type: 'quantitative', title: 'Context Length', axis: { format: '~s' } },
              y: { field: 'cum_cost_total', type: 'quantitative', title: 'Cumulative Cost ($)', axis: { format: '$,.0f' }, scale: { domain: [0, 30] } },
              color: { field: 'conversation_id', type: 'nominal', legend: null },
              opacity: { condition: { param: 'hover', empty: false, value: 1 }, value: 0.3 },
              strokeWidth: { condition: { param: 'hover', empty: false, value: 6 }, value: 1 },
              tooltip: [
                { field: 'conversation_id', title: 'Conversation' },
                { field: 'turn', title: 'Turn' },
                { field: 'monotonic_context', title: 'Context', format: ',.0f' },
                { field: 'cum_cost_total', title: 'Total Cost', format: '$,.2f' },
                { field: 'cum_cost_cache_read', title: 'Cache Read Cost', format: '$,.2f' }
              ]
            }
          },
          {
            data: { values: endpoints },
            mark: { type: 'text', align: 'left', dx: 5, fontSize: 11 },
            encoding: {
              x: { field: 'monotonic_context', type: 'quantitative' },
              y: { field: 'cum_cost_total', type: 'quantitative' },
              text: { field: 'cum_cost_total', format: '$,.2f' },
              opacity: { condition: { param: 'hover', empty: false, value: 1 }, value: 0 }
            }
          }
        ]
      },
      {
        width: 340,
        height: 250,
        title: 'Cache Read Cost',
        layer: [
          {
            data: { values: data },
            mark: { type: 'line', strokeWidth: 1.5 },
            encoding: {
              x: { field: 'monotonic_context', type: 'quantitative', title: 'Context Length', axis: { format: '~s' } },
              y: { field: 'cum_cost_cache_read', type: 'quantitative', title: 'Cumulative Cost ($)', axis: { format: '$,.0f' }, scale: { domain: [0, 30] } },
              color: { field: 'conversation_id', type: 'nominal', legend: null },
              opacity: { condition: { param: 'hover', empty: false, value: 1 }, value: 0.3 },
              strokeWidth: { condition: { param: 'hover', empty: false, value: 6 }, value: 1 },
              tooltip: [
                { field: 'conversation_id', title: 'Conversation' },
                { field: 'turn', title: 'Turn' },
                { field: 'monotonic_context', title: 'Context', format: ',.0f' },
                { field: 'cum_cost_total', title: 'Total Cost', format: '$,.2f' },
                { field: 'cum_cost_cache_read', title: 'Cache Read Cost', format: '$,.2f' }
              ]
            }
          },
          {
            data: { values: endpoints },
            mark: { type: 'text', align: 'left', dx: 5, fontSize: 11 },
            encoding: {
              x: { field: 'monotonic_context', type: 'quantitative' },
              y: { field: 'cum_cost_cache_read', type: 'quantitative' },
              text: { field: 'cum_cost_cache_read', format: '$,.2f' },
              opacity: { condition: { param: 'hover', empty: false, value: 1 }, value: 0 }
            }
          }
        ]
      }
    ],
    config: { concat: { spacing: 20 } }
  };

  vegaEmbed(selector, spec, { actions: false });
}

// Renders the cost line chart
async function renderCostChart(csvUrl, selector) {
  const { costRecords, maxX } = await loadTokenData(csvUrl);

  // Get unique x positions for hover points, combining all metrics at each x
  const hoverPoints = [];
  const byX = {};
  for (const r of costRecords) {
    if (!byX[r.x]) byX[r.x] = {x: r.x, msg_idx: r.msg_idx};
    if (r.metric === 'Total Cost') byX[r.x].totalCost = r.value;
    if (r.metric === 'Cache Read Cost') byX[r.x].cacheReadCost = r.value;
    if (r.metric === '% Cache Read') byX[r.x].pctCacheRead = r.pct;
  }
  for (const x in byX) hoverPoints.push(byX[x]);

  const spec = {
    "$schema": "https://vega.github.io/schema/vega-lite/v5.json",
    "width": 700, "height": 150,
    "layer": [
      {
        "data": {"values": costRecords},
        "transform": [{"filter": "datum.metric === 'Total Cost' || datum.metric === 'Cache Read Cost'"}],
        "mark": {"type": "line", "strokeWidth": 2},
        "encoding": {
          "x": {"field": "x", "type": "quantitative", "title": "Context Position (tokens)", "scale": {"domain": [0, maxX]}},
          "y": {"field": "value", "type": "quantitative", "title": "Cumulative Cost ($)"},
          "color": {"field": "metric", "type": "nominal", "scale": {"domain": ["Total Cost", "Cache Read Cost", "% Cache Read"], "range": ["steelblue", "orange", "green"]}, "legend": {"title": null}}
        }
      },
      {
        "data": {"values": costRecords},
        "transform": [{"filter": "datum.metric === '% Cache Read'"}],
        "mark": {"type": "line", "strokeWidth": 2, "strokeDash": [4, 4]},
        "encoding": {
          "x": {"field": "x", "type": "quantitative"},
          "y": {"field": "pct", "type": "quantitative", "title": "% Cache Read", "axis": {"orient": "right", "grid": false}},
          "color": {"datum": "% Cache Read"}
        }
      },
      {
        "data": {"values": hoverPoints},
        "params": [{
          "name": "hover",
          "select": {"type": "point", "on": "mouseover", "nearest": true, "clear": "mouseout"}
        }],
        "mark": {"type": "rule", "strokeWidth": 1, "stroke": "#999"},
        "encoding": {
          "x": {"field": "x", "type": "quantitative"},
          "opacity": {"condition": {"param": "hover", "empty": false, "value": 0.7}, "value": 0},
          "tooltip": [
            {"field": "msg_idx", "title": "Message"},
            {"field": "x", "title": "Context Position", "format": ","},
            {"field": "totalCost", "title": "Total Cost", "format": "$.2f"},
            {"field": "cacheReadCost", "title": "Cache Read Cost", "format": "$.2f"},
            {"field": "pctCacheRead", "title": "% Cache Read", "format": ".1f"}
          ]
        }
      }
    ],
    "resolve": {"scale": {"y": "independent"}}
  };

  vegaEmbed(selector, spec, {actions: false});
}
