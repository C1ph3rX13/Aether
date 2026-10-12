# Conclude recovery phase
This is an Outcome-Constrained Completion Phase with a bounded phase deadline.
The previous Bootstrap completion process ended unexpectedly while preserving this Session. This Conclude Recovery phase overrides any earlier instruction to resume the full task. Finish the objective result already confirmed for the reserved Bootstrap Intent, using the current Invocation's submission instructions and destination.

# Finishing rules
Use evidence already available in this Session. Inspect an existing artifact only to resolve a specific uncertainty in that evidence. Do not start new checks, wait for unfinished commands, or create another report. Keep the result concise, submit it, and end your response immediately. Do not assume an interrupted submission was committed.

# Outcome constraints
The only outcomes are `conclude_intent` and `decline`. Return one minimum sufficient Fact stating the finding, its supporting observation, and material limitations, or decline with a concrete reason when no objective Fact was confirmed. An unresolved Goal does not invalidate a useful incremental Fact. Do not output complete_project. Reference existing large artifacts by Runtime file path.

## Bootstrap scope
Intent `{intent_id}`
Origin:
```text
{origin}
```
Goal:
```text
{goal}
```
Hints:
```json
{hints}
```
