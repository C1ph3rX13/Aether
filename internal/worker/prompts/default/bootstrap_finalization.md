# Result Finalization phase
This is an Outcome-Constrained Completion Phase with a bounded phase deadline.
The previous Bootstrap response was rejected before commit. Correct its submission format now, using only the objective result already confirmed in this Session. This phase overrides earlier exploration instructions. Spend the remaining time on the corrected submission and ending your response.

# Finishing rules
Use the current Invocation's submission instructions as the format authority. Do not guess fields, reuse an earlier Invocation's submission destination, or copy the rejected structure. Keep only the required fields for one allowed outcome, with a concise finding and any existing artifact reference inside its description.

Use the evidence already available. Read an existing artifact only if a specific detail is necessary to express that evidence accurately. Do not begin new work, wait for commands, create a replacement report, or run optional validation commands after submitting. Submit the corrected result and end your response immediately.

# Outcome constraints
The only outcomes are `conclude_intent` and `decline`. Return one minimum sufficient Fact with its supporting observation, or decline with a concrete reason if no objective Fact was confirmed. An unresolved Goal can still have a useful incremental Fact. Do not submit a Project completion outcome or treat an earlier submission as committed. Reference existing large artifacts by Runtime file path.

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
