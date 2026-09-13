# Domain Docs

## Before exploring, read these

- Root-level `CONTEXT.md`.
- Relevant ADRs under `docs/adr/`.

If a file is absent, proceed silently. Domain-modeling skills create documentation lazily when terms or decisions are resolved.

## Layout

This is a single-context repository:

/
├── CONTEXT.md
└── docs/adr/

## Use the glossary’s vocabulary

Use concepts exactly as defined in `CONTEXT.md`, avoiding synonyms that the glossary explicitly rejects. Note genuine vocabulary gaps for domain modeling.

## Flag ADR conflicts

Explicitly surface outputs that contradict an existing ADR rather than silently overriding it.
