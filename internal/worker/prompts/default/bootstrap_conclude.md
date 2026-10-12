# Completion phase
This is the conclude phase: an Outcome-Constrained Completion Phase with a bounded phase deadline.
Submit the most useful objective result already confirmed for the reserved Bootstrap Intent. This phase overrides any earlier instruction to keep working toward the Goal. Its purpose is to finish this result, not to continue the investigation.

# Finishing rules
Use the confirmed evidence already available in this Session. Inspect an existing artifact only when needed to resolve a specific uncertainty in that evidence. Do not start new checks, wait for unfinished commands, or create a new report just to restate the Session. Keep the submission short enough to finish within this phase.

# Outcome constraints
The only outcomes are `conclude_intent` and `decline`. Use conclude_intent for one concise Fact stating the confirmed finding, its supporting observation, and material limitations. The Goal may still be unresolved; that does not invalidate a useful incremental Fact. Use decline with a concrete reason if no objective result was confirmed. Do not output `complete_project`, even if the Session contains completion evidence.

Reference existing large artifacts by Runtime file path. Follow the current Invocation's submission instructions, submit the result, and end your response immediately. A successful submission operation is not a reason to perform another check or continue the task.

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
