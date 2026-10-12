# Task
Choose the next useful operation for this Project from the supplied evidence. Read the immutable Blackboard snapshot for this Run, understand the Origin and Goal, and evaluate confirmed Active Facts, Open Intents, and current Active Hints. Reason makes a decision from that evidence; Explore performs new investigations.

# Decision rules
1. Reconcile the Goal with all supplied Active Hints, including Reopen Feedback. Interpret each Hint as guidance or a request for investigation or verification. A previous completion or an old Fact does not answer a request for fresh verification. Hints guide work; they are not evidence and do not automatically add new Goal conditions.
2. Decide whether confirmed Active Facts prove the Goal and address the investigation or verification requested by current Active Hints. If evidence is missing, identify the smallest independent investigation that would produce it. If the current work has drifted away from the Goal, choose a direction that corrects it.
3. Check whether Open Intents already cover the missing evidence. Compare investigation scope and evidence questions, not just wording or source IDs. If the existing Open Intents cover the unresolved work, return no_change unless a clearly separate direction is needed.
4. When proposing an Intent, use only eligible source Fact IDs and propose no more than {max_intents} independent directions. Describe the evidence question, the scope, and what observation would answer it. Prefer the smallest useful investigation over a broad task to solve the whole Goal.
5. Base the decision on the supplied snapshot and existing artifacts. Do not perform new investigations or turn a Hint, a plan, or an unverified expectation into completion evidence. Use source Fact IDs exactly as supplied; Intent IDs, Hint IDs, historical completions, and artifact paths are not source Fact IDs.

# Outcome choices
- complete_project: use only when confirmed Active Facts prove the Goal and address current requests for investigation or verification. Cite one or more eligible source Fact IDs and explain the proof briefly.
- propose_intents: use for missing evidence. Each Intent needs eligible source Fact IDs and a focused description.
- no_change: use when an existing Open Intent already covers the unresolved work and no additional direction is needed. When the current limit allows proposals, missing evidence with no covering Intent calls for propose_intents. If no covering Intent exists and the proposal limit is zero, decline with that concrete limitation.
- decline: use when the task cannot be performed in the available scope.

Choose one outcome and follow the current Invocation's submission instructions. Keep descriptions focused on the decision and its evidence, then end your response.

# Context
Read the immutable Blackboard snapshot from the file path supplied with this task.

## Current Active Hints
Use all Active Hints supplied here as current guidance. Withdrawn Hints are historical context only:
```json
{hints}
```

## Eligible source Fact IDs
Only these active Fact IDs may appear as Intent sources or completion evidence:
```json
{fact_ids}
```

## Open Intents
These Intents have not reached a conclusion. Use them to avoid duplicate work:
```json
{open_intents}
```
