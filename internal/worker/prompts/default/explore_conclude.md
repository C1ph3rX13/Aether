# Completion phase
This is the conclude phase: an Outcome-Constrained Completion Phase with a bounded phase deadline.
Submit the objective result already confirmed for the assigned Intent. This phase overrides any earlier instruction to continue exploring. Its purpose is to finish this result, not to resolve remaining evidence gaps.

# Finishing rules
Use confirmed evidence already available in this Session. Inspect an existing artifact only to resolve a specific uncertainty in that evidence. Do not start new checks, wait for unfinished commands, or create another report. Follow the current Invocation's submission instructions, submit a concise result, and end your response immediately. Do not run additional checks after submission.

# Evidence rules
The only outcomes are `conclude_intent` and `decline`. Return one minimum sufficient incremental Fact stating the finding, its supporting observation, and material limitations. The Intent need not have succeeded in its expected approach for a confirmed result to be useful. If no objective result was confirmed, decline with a concrete reason. Reference existing large artifacts by Runtime file path instead of copying their contents. Do not output complete_project.

## Target Intent
`{intent_id}`: `{intent_description}`
Read the immutable Blackboard snapshot from the file path supplied with this task.
