# Session goals

Only an explicit user `/goal <objective>` starts a durable goal for the current conversation and immediately submits its first model turn; ordinary conversations are never converted into goals. Use `/goal status` to inspect it, `/goal pause` to stop the active run, `/goal resume` to explicitly continue a paused or restart-interrupted goal, and `/goal clear` to remove it. A saved goal never resumes automatically after pk restarts.

While a goal is active, pk exposes `GoalComplete` and `GoalBlocker` to the model. Completion requires concrete verification evidence. A blocker is recorded across distinct goal turns; three consecutive reports of the same blocker mark the goal blocked and await the user. When an assistant turn ends without tool work, pk allows up to three consecutive attempts to continue; tool work resets that count. Provider errors, cancellation, and a paused or cleared goal stop the loop. Productive work has no fixed turn limit.

Goals are scoped to the saved session and stored under `PK_HOME/goals`. The continuation loop uses the session's existing workspace, provider, tools, and operation policy. It does not grant approval, bypass tool restrictions, or provide an OS sandbox. There is no goal token budget control in this version.
