# Daily Brief Usage

Requires: `ANTHROPIC_API_KEY` env var, `deno` on PATH.

## Trial run

```sh
uv run scripts/daily_brief.py --date 2026-02-27          # stdout only
uv run scripts/daily_brief.py --date 2026-02-27 --verbose # with REPL traces
```

Output writes to `brief_YYYY_MM_DD.md` in the repo root.

## View traces

Every run is logged to `mlruns.db` via MLflow.

```sh
uv run --with mlflow mlflow ui --backend-store-uri sqlite:///mlruns.db
```

Open http://127.0.0.1:5000. Each run shows the full RLM trajectory: REPL code the model wrote, what it printed, sub-LLM calls, and the final brief.

## dSPy optimization

dSPy optimizers (MIPROv2, etc.) work on RLM modules as a drop-in. Example:

```python
import dspy
from daily_brief import DailyBrief

lm = dspy.LM("anthropic/claude-sonnet-4-5-20250929")
dspy.configure(lm=lm)

rlm = dspy.RLM(DailyBrief, max_iterations=15, max_llm_calls=30)

# Build a trainset of (inputs, expected_brief) examples
trainset = [
    dspy.Example(
        date="2026-02-27",
        n_commits=51,
        commits=commits_feb27,
        history=history_feb27,
        brief=known_good_brief_feb27,
    ).with_inputs("date", "n_commits", "commits", "history"),
    # ...more examples
]

optimizer = dspy.MIPROv2(metric=your_metric, auto="medium")
optimized = optimizer.compile(rlm, trainset=trainset)
optimized.save("optimized_daily_brief.json")
```

The metric function scores candidate briefs (e.g. LLM-as-judge comparing against a gold brief, or heuristic checks for link format / length / coverage). Optimized prompts and parameters save to JSON and can be loaded back with `rlm.load()`.

## "Deployment"

Currently runs on chicken.exe.xyz, in an unhelpfully ad hoc way.

Probably will be eclipsed in the future and moved somewhere better.
