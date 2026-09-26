**Operations that must not act unasked.** `mnemo_rename` and `mnemo_forget` never act on the first call: they report
what would happen and hand back a confirmation value.

{{CONFIRM_NOTE}}

`mnemo_push` is different: it publishes on the first call, so ask the user before calling it at all unless
`mnemo_status` reports autopush on. When its secret scan refuses, the findings name a file and a line with the match
redacted. Show them, and pass the value back as `acknowledge` — not `confirm` — only if the user confirms it is a
false positive. A credential published to a hub is not something an acknowledgement can take back.
