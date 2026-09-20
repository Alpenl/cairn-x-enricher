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
