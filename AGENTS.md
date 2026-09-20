# Project guidance

- For Jev/TypeSafe integration or classification changes, read the project-local
  `.agents/skills/typesafe-ai/SKILL.md` and its relevant current API references.
- Production processing separates source retrieval, reading aids, and classification.
  Persist source snapshots before generating reading aids. Only the classification
  queue may write Jev suggestions; never overwrite human curation.
- The Worker contract and migration live in `../cairn-share/worker`.
  Coordinate schema/API changes across both repositories and preserve unrelated edits.
- Keep `.env` ignored. Never print or commit credential values.
- Use `shnote --what "<action>" --why "<reason>" run <command>` for shell operations.
  Pure `cat`, `head`, `tail`, `sed -n`, and `nl -ba` reads may run directly.
- Verification: `make verify`; Worker changes also require `npm test` and `npm run typecheck`.
  Live model tests require explicit opt-in and are excluded from normal checks.
- Jev v2 work is tracked by `Alpenl/cairn-x-enricher#10` and its linked implementation
  issues. Read `docs/jev-v2/ISSUE-INDEX.md` and `EXECUTION.md`. Issues own progress;
  repository documents own versioned design; implementation PRs own code and evidence.
  The former plan-only PR branches are historical, not an implementation stack.
  Preserve task IDs, use ordinary issue references for partial PRs, and do not merge,
  deploy, close implementation issues, or run paid models without the required approval.
