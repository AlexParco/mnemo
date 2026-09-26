**Machine-bound work.** Memory is shared across the user's machines, but some pending items belong to one only.
Those are stamped `[@<machine>]`, and the card shows which machine they belong to when it is not this one.

Do not act on them here: do not look for their repo, do not commit or push them. Say the work belongs to that
machine instead. Before any git or build action on a code repo, check it exists on this machine.

When saving, stamp `[@<machine>]` ONLY on what is physically tied to one machine: uncommitted changes, a local
unpushed branch, a service running there, a local path. Test: could any machine that has the repo do it? Then it
is portable — do not stamp it. When unsure, do not stamp: a portable item wrongly bound to one machine is as
confusing as a local one left unmarked.
