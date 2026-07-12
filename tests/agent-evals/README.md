# Agent eval harness

`tests/agent-evals` runs the seven v1 agent-operability scenarios from
`docs/ship-spec.md` section 9, including `approval-recovery`, against a
Docker-backed fake VPS.

Each scenario:

1. starts a fresh fake-vps box;
2. creates a scratch Git repo with a `ship.toml`;
3. induces the scenario failure;
4. gives a runner the one-line goal, the induced failure output, and `ship docs`;
5. executes emitted shell commands in the project directory;
6. passes only if the scenario checker succeeds within 6 tool calls.

Transcripts are written to `tests/agent-evals/transcripts/`.

## Oracle mode

Oracle mode is the default when `SHIP_EVAL_AGENT_CMD` is unset. It needs no API.

The oracle reads the latest `next: ...` remediation from the current command
output and executes it mechanically. It performs only these substitutions:

- `<message>` becomes `ship eval remediation`;
- `KEY` in `ship secret set KEY` becomes the missing secret named in the error
  text;
- `ship secret set ...` receives a deterministic stdin value.
- a scenario may declare one fix command for remediation that requires judgment.
  The oracle may run that fix only after the `next:` chain reaches the
  scenario's exact actionable guidance, usually via `ship why`.

If a `next:` line is not executable, has an unresolved placeholder, loops, or
does not lead to a passing checker, the run records an `ORACLE_FINDING` and
keeps the transcript. That is an error-text finding, not a weakened checker.

Run oracle evals:

```sh
SHIP_RUN_FAKE_VPS_SMOKE=1 go test ./tests/agent-evals -run TestAgentEvalScenarios -count=1 -timeout 30m
```

## Real agent mode

`make agent-evals` requires `SHIP_EVAL_AGENT_CMD` and sets
`SHIP_EVAL_RUNNER=agent`.

`SHIP_EVAL_AGENT_CMD` is a shell command template. The harness runs it from the
scratch project directory. The prompt is always sent on stdin. The command may
also use these placeholders, shell-quoted by the harness:

- `{prompt}`: full prompt text;
- `{prompt_file}`: path to a file containing the full prompt;
- `{context_file}`: path to a file containing `ship docs`;
- `{goal}`: the one-line scenario goal;
- `{workdir}`: project directory;
- `{turn}`: 1-based turn number;
- `{last_output_file}`: path to command history so far.

Examples:

```sh
SHIP_EVAL_AGENT_CMD='my-agent --prompt-file {prompt_file}' make agent-evals
SHIP_EVAL_AGENT_CMD='my-agent --goal {goal} --context {context_file}' make agent-evals
SHIP_EVAL_AGENT_CMD='my-agent' make agent-evals
```

Agent stdout must contain shell commands. Blank lines, comments, `$ ` prompts,
and markdown fences are ignored.

Modes:

- `SHIP_EVAL_AGENT_MODE=turn` (default): invoke the agent after each batch of
  command output. Every emitted non-empty command line is executed until the
  checker passes or the 6-call limit is reached.
- `SHIP_EVAL_AGENT_MODE=script`: invoke the agent once. Its stdout is treated
  as a newline-delimited command script. Each command line still counts as one
  tool call.
