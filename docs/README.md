# Klyx docs

Start with `CLAUDE.md` at the repository root: what Klyx is, its locked decisions and its milestone history. Then:

| Where | What |
|---|---|
| [`design-principles.md`](design-principles.md) | The design philosophy and the visual language in force (The Line). |
| [`backlog.md`](backlog.md) | The living status: the v1.0.0 release gate, the shipped log, what's left. |
| [`superpowers/`](superpowers/README.md) | One spec per feature or slice, with its review revisions and as-built notes, and the plans; indexed by programme. |
| [`releases/`](releases/) | Release notes, one file per version. |
| [`checklists/`](checklists/) | The native eyeball checklists the v1.0.0 gate refers to. |
| [`design/`](design/) | The design record: the June mockups, the three July directions with Signal Bench's notes and tokens, and the redesign prompt. Signal Bench's visuals are retired; The Line replaced them in v0.14.0. |
| [`history/`](history/) | The founding prompt, rewritten with hindsight, and the June open questions. |

How work is recorded: a spec in `superpowers/specs/` is written and reviewed before it is built, and gains an as-built section once built; release notes are written when a version is tagged; `backlog.md` is pruned as work ships. Review reports (`docs/reviews/`) and working files stay out of the repository.
