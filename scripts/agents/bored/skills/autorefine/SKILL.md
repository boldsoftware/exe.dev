---
name: autorefine
description: Use when the user mentions "autorefine".
---

# Autorefine

Autorefinement polishes code through an iterative process of independent code review and fix.

If the user asks you to autorefine, sometimes by using the single word "autorefine" with no other comments, their goal is to be hands-off for a while and return to substantially improved code. The user may add additional instructions for the code review agents, such as "autorefine 5 with a focus on concurrency". In that case, everything after autorefine is intended to be passed along, including the number 5, which has special handling by the codereview skill.

Generate a short random hex string (8 chars) as a run ID for this autorefine session.

Up to 25 times (although rarely will all 25 be needed):

- save a ref to the current commit: `git update-ref refs/autorefine/<branch>/<run-id>/<iteration> HEAD`, 0-indexed
- invoke bash with this command: cco -p 'Using the codereview skill, please codereview [insert instructions for code reviewers here]. When it comes time for handling the review, do full autopilot.'
- wait for it to complete; this is expected to be slow, use a very long timeout and do not poll or monitor the intermediate output; read only the final output when it lands
- stop iterating iff one of these is true:
  - five consecutive runs are clearly flapping or failing to converge
  - three consecutive runs show clear diminishing returns, such as "ship it" or minor fussing over comments

At the end, prepare a summary for the user. Include the first and last refs (`refs/autorefine/<branch>/<run-id>/0` and `.../<n>`) so the user can inspect individual steps. (The refs can also be helpful to you if user asks to revert one or more changes.)

The summary is _not_ a discursive history of the process. Rather, the goal of the summary is to _teach_ the user about the interesting design decisions made along the way, so that the user has a rich mental model of the work and its most consequential or controversial aspects. That will enable them to assess the direction, maintain their understanding of the broader system, and review the diff effectively and efficiently.
