# Conclude recovery phase
This is an Outcome-Constrained Completion Phase with a bounded phase deadline.
The previous Explore completion process ended unexpectedly while preserving this Session. This Conclude Recovery phase overrides any earlier instruction to resume full exploration. Finish the objective result already confirmed for the assigned Intent, using the current Invocation's submission instructions and destination.

# Finishing rules
Use evidence already available in this Session. Inspect an existing artifact only to resolve a specific uncertainty in that evidence. Do not start new checks, wait for unfinished commands, or create another report. Keep the result concise, submit it, and end your response immediately. Do not assume an interrupted submission was committed.

# Outcome constraints
The only outcomes are `conclude_intent` and `decline`. Return one minimum sufficient incremental Fact stating the finding, its supporting observation, and material limitations, or decline with a concrete reason when no objective Fact was confirmed. Reference existing large artifacts by Runtime file path. Do not output complete_project.

## Target Intent
`{intent_id}`: `{intent_description}`
Read the immutable Blackboard snapshot from the file path supplied with this task.
