---
title: "Expensively Quadratic: the LLM Agent Cost Curve"
description: cache reads are quadratic and dominate your long agentic conversations
author: Philip Zeyliger
date: 2026-02-02
tags:
  - foo
published: false
---

<style>
main { max-width: 900px; }
</style>

Pop quiz: at what point in the context length of a coding agent are cached
reads costing you half of the next API call? It’s complicated, but by
50,000 tokens, your conversation’s costs are probably being dominated by cache reads.

Let’s take a step back. We’ve [previously
written](https://sketch.dev/blog/agent-loop) about how coding agents work. To
recap, they post the conversation thus far to the LLM, and continue doing that in
a loop as long as the LLM is requesting tool calls. When there are no
more tools to run, the loop waits for user input, and the whole cycle starts
over. Visually:

<object type="image/svg+xml" data="/assets/agentic-loop.svg" style="max-width: 50%; height: auto;">The agentic loop</object>

Or, in code form:

```python
def loop(llm):
    msg = user_input()
    while True:
        output, tool_calls = llm(msg)
        print("Agent: ", output)
        if tool_calls:
            msg = [handle_tool_call(tc)
                   for tc in tool_calls]
        else:
            msg = user_input()
```


The LLM providers charge you for input tokens, cache writes, output tokens, and cache reads. It’s a little tricky: you indicate in your prompt to cache up to a certain point (usually the end), and you get charged as “cache write” and not input. The previous turn's output becomes the next turn's cache write. Visually:

<object type="image/svg+xml" data="/assets/quadratic-tokens.svg" style="max-width: 100%; height: auto;">Token costs across LLM calls</object>

Here, the colors and numbers indicate the costs making up the nth call to the
LLM. Every subsequent call reads the story so far from the cache, writes the
previous call’s output to the cache (as well as any new input), and gets an
output. The area represents the cost, though it's not drawn to scale.

That triangle emerging for cache reads? That's the scary quadratic!

How scary is the quadratic? Pretty squarey! I took a rather ho-hum feature
implementation conversation, and visualized it like the diagram above. The area
corresponds to cost: the width of every rectangle is the number of tokens and
the height is the cost per token.

<script src="https://cdn.jsdelivr.net/npm/vega@5"></script>
<script src="https://cdn.jsdelivr.net/npm/vega-lite@5"></script>
<script src="https://cdn.jsdelivr.net/npm/vega-embed@6"></script>
<script src="/assets/quadratic.js"></script>

<div id="token-viz"></div>
<script>renderTokenRectangles('/assets/quadratic.csv', '#token-viz');</script>

The whole conversation cost $12.93 total or so. You can see that as the
conversation continues, the cache reads dominate. At the end of the
conversation, cache reads are 87% of the total cost. They were half the cost at
27,500 tokens!

<div id="cost-viz"></div>
<script>renderCostChart('/assets/quadratic.csv', '#cost-viz');</script>

This conversation above is just one example. Does this happen generally?
exe.dev's LLM gateway keeps track of the costs we're incurring, so I was able
to pull together the "cumulative cost" visualization for many Shelley
conversations, not just my own. I sampled 250 conversations from my data randomly.

The x-axis is the context length, and the y-axis is the cumulative cost up to
that point. The left graph is all the costs and the right graph is just the
cache reads. You can mouse over to find a given conversation on both graphs.

<div id="conversation-costs"></div>
<script>renderConversationCosts('/assets/ pus_conversations.json', '#conversation-costs');</script>

The graphs are all different because every conversation is different. Some
conversations write a lot of code, so spend more money on expensive output
tokens. Some conversations read lots of the code base, so spend money on tool
call outputs, which look like cache writes. Some conversations waited for the
user while the cache expired, so have to re-write data to the cache. In our
data, the median input was about 250 tokens and the median output was about
100, but the distribution is wide.

Let's look at how those conversations got to 100,000 tokens. You'll
recognize that the number of LLM calls in the conversation matters
quite a bit. The cache read cost isn't really the number of tokens
squared; it's the number of tokens times the number of calls,
and different conversations have very different numbers of LLM calls!

<div id="cost-at-100k"></div>
<script>renderCostAt100k('/assets/cost_at_100k.json', '#cost-at-100k');</script>

To go back to our original question, we can build a little simulator.
Anthropic's rates are *x* for input, 1.25*x* for cache write, 5*x* for output, and
*x*/10 for cache read, where *x* = $5 per million tokens.  In the default settings
of the simulator, it only takes 20,000 tokens to get to the point where cache reads
dominate.

<div id="simulator"></div>
<script>renderCostSimulator('#simulator');</script>

<p>

The basic agentic loop is quadratic, and there's a lot of subtlety:

* All things equal, taking fewer LLM calls is going to be cheaper than
  more LLM calls, even if they produce the same number of expensive output
  tokens. Agents should be encouraged to batch up their reading and exploratory
  commands.
* Long conversations are expensive. If you're at, say, 75k, you're better off
  asking a new continuation question, and re-spending tokens on re-establishing
  context, rather than naively continuing in the same conversation.

