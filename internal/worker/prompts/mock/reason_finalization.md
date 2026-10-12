# Completion phase
This is an Outcome-Constrained Completion Phase with a bounded phase deadline.
The previous Reason result was not committed. Correct its submission format now, using only the decision already established from this Session's confirmed evidence. This phase overrides earlier instructions to keep exploring or make new decisions.

# Finishing rules
Use the current Invocation's submission instructions as the format authority. Do not guess fields, reuse an earlier Invocation's submission destination, or copy a rejected structure. Keep only the required fields for one allowed outcome. Check source Fact IDs and the Intent count against the supplied lists and limit; an intended completion must still have sufficient evidence.

Use the evidence and decision already available. Inspect an existing artifact only if a specific detail is necessary to express that decision accurately. Do not investigate new directions, wait for commands, or run optional validation commands after submitting. Submit the corrected result and end your response immediately.

# Outcome constraints
The admitted outcomes are `complete_project`, `propose_intents`, `no_change`, and `decline`. propose_intents is available only when the current limit is positive, and may contain at most {max_intents} Intents, each with eligible source Fact IDs and a focused description. Do not duplicate existing Open Intents. Use no_change only when existing Open Intents cover the unresolved work. complete_project requires confirmed Active Facts proving the Goal and addressing current requests for investigation or verification; Hints and historical Completion Intents are not evidence. If no supported decision was established, return decline with a concrete reason. Do not treat an earlier submission as committed.

## Current Active Hints
```json
{hints}
```

## Eligible source Fact IDs
```json
{fact_ids}
```

## Open Intents
```json
{open_intents}
```
