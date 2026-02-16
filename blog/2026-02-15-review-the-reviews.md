---
title: Review the reviews
description: The code reviewer reviewer reviews the code reviewer's reviews.
author: Josh Bleecher Snyder
date: 2026-02-15
tags:
  - ai
published: true
---

When I was actively contributing to the Go project, my primary feed was the [code review](https://go-review.googlesource.com/) email firehose.

Issues, mailing lists, and Slack had low SNR. The finished commit history was better: it was finely polished work with some of the best written commit messages I have ever encountered. But it didn't hold a candle to code reviews for operational learning.

The commit history could tell you what got done and why, in impressive technical detail. But the code review could also tell you about mid-stream direction changes; what concerns were taken seriously; what got started and not completed; what values drove decisions.

---

I'm reflecting on that this morning because of [Simon Willison's comments about cognitive debt](https://simonwillison.net/2026/Feb/15/cognitive-debt/):

> I've been experimenting with prompting entire new features into existence without reviewing their implementations and, while it works surprisingly well, I've found myself getting lost in my own projects.
>
> I no longer have a firm mental model of what they can do and how they work, which means each additional feature becomes harder to reason about, eventually leading me to lose the ability to make confident decisions about where to go next.

Having a human in the loop benefits the human. But where?

Reading code reviews was effective for the Go project. It still is.

---

My workflow has churned over the last year. But here's what I do now: I review reviews.

I ask an agent to do something. Code happens. I then ask an agent to review that code, without looking at it myself. Then I review the review.

The agent's review typically contains design commentary, questions about decisions made, bugs, and nits. This is usually enough for me to get a clear idea about what's going on in the code, at the right level of abstraction. And it enables me to very efficiently provide direction.

I have a heavily used code review skill that optimizes for this workflow:

> Number all comments, questions, and suggestions for easy reference.
> Use an ever-incrementing scheme starting at 1.
>
> Format:
>
> - Top-level items: `1.`, `2.`, `3.`
> - Sub-items: `2a.`, `2b.`
>
> This lets the user respond concisely and unambiguously: "3: please fix" or "2b: stet"

An agent who has just done a code review has an ideally primed context window for working on that code. It makes fixes for me.

And as you might have guessed, when those fixes are done, I amend the commit unseen and start another code review cycle. When the code reviews stabilize, I skim the final commit. There are rarely any surprises.

The reviews rarely actually come back clean. Rather, they converge on commentary I've already decided to ignore, places where the model weights and I flatly disagree.

---

The numbers bear this out. I just asked Claude to look over the entire history of the initial prompts I give it and do some light analysis.

> Top 1-grams: [`please`](https://commaok.xyz/ai/manners/) (2.03%), `codereview` (0.64%), `look` (0.61%), `use` (0.58%), `make` (0.51%)
>
> Top 2-grams: `look at` (0.41%), `please codereview` (0.39%), `i want` (0.34%), `want to` (0.29%), `add a` (0.21%)

I learned while writing this blog post that "code review" is two words. RIP stats.

---

My life is now officially Seussian. I watch the watchers.

<img src="/assets/beewatcher.jpg" alt="The Bee Watcher" style="max-width: 100%; height: auto; border-radius: 8px;" />

*Cross-posted at [commaok.xyz/ai/review-the-reviews/](https://commaok.xyz/ai/review-the-reviews/)*
