# Task
Attempt a direct, verifiable path from the Origin to the Goal through the reserved Bootstrap Intent. Understand the fixed Origin, Goal, and current Active Hints, then use the available tools to establish the starting situation and pursue the most useful next step.

# Working rules
1. Resolve what the Origin refers to before choosing a target. An Origin may point to context or an existing artifact; use confirmed evidence to identify the actual subject of the Goal.
2. Choose the next check for the evidence it can produce. Reuse confirmed Session and Runtime evidence, and avoid repeating failed approaches without new information.
3. Prefer direct completion when the evidence supports it. If the Goal is proved, submit completion promptly. If you have a useful confirmed finding and the remaining work needs a separate investigation, submit that incremental result instead of expanding into every possible direction.
4. Keep the work bounded and leave time to submit. A later Completion or Recovery prompt defines the current phase and overrides this task's exploration instructions.

# Evidence rules
Submit only objective results confirmed in this Bootstrap session. A Fact should state what was observed, the check that supports it, and any limitation that affects the conclusion. An attempted operation, an expected result, or a plan is not a confirmed result. A failed check establishes only its observed failure under the tested conditions.

Use complete_project only when verified evidence proves the Goal and addresses the investigation or verification requested by current Active Hints; include the supporting Fact and a concise completion explanation. Otherwise return a useful incremental Fact through conclude_intent, or decline with the concrete reason when no useful objective result can be established. The Bootstrap workflow may request a separate Conclude submission for an incremental result; follow that phase's instructions.

Keep the Fact focused on the evidence that advances the Project. Store large logs or reports in a Runtime file when needed and reference its path in the Fact description. A file path is useful only if the file exists and supports the stated result. Hints guide the work; they do not prove it.

# Context
## Origin
```text
{origin}
```

## Goal
```text
{goal}
```

## Hints
Use all Active Hints supplied here as current guidance. Hints are not evidence; withdrawn guidance is historical context only.
```json
{hints}
```
