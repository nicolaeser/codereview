# `.ai/` Governance

This manual applies only when auditing or modifying the repository context system. [BASE.md](BASE.md) remains the universal entry point, and [SUMMARY.md](SUMMARY.md) is the exhaustive active-document index.

## Purpose and authority

The mandatory core is:

- `BASE.md`: concise universal conduct, safety, precedence, routing, and completion requirements; it is the only always-loaded project entry point.
- `AI.md`: governance for creating and maintaining this system; it is not ordinary task context.
- `STYLE.md`: the canonical presentation, interaction, accessibility, and documentation contract, including an explicit limited scope when the repository has no owned visual UI.
- `SUMMARY.md`: the pure discovery index for every other active `.ai/` Markdown document.

Optional categories have these exclusive roles:

- **Knowledge:** verified terminology, system facts, decisions, current limitations, and durable experience that are not adequately obvious from canonical sources.
- **Instructions:** persistent mandatory rules for stable, broad engineering domains.
- **Playbooks:** repeatable repository procedures with ordered validation and a definition of done.
- **Personas:** optional specialist review methods, decision priorities, checklists, and expected outputs that apply authoritative rules without restating them.

Neither category presence nor file count is a quality target. Do not create empty directories, placeholders, generic filler, or documents merely to make the tree look complete.

## Evidence gate

Create or retain an optional document only when all of the following hold:

1. It has a distinct activation condition likely to recur in this repository.
2. Its content is supported by one authoritative enforced source or multiple consistent current sources.
3. It materially improves implementation, review, debugging, or operational reliability.
4. Its authoritative content is not already covered adequately by another `.ai/` document or canonical repository source.
5. It can remain concise and repository-specific without generic guidance.

For a Persona, item 4 applies to its specialist method and expected output rather than to the rules it applies. An Instruction or Playbook is not, by itself, a reason to reject a distinct Persona.

## Document conventions

- Write concise professional English and use **must**, **must not**, **should**, and **may** consistently.
- Use lowercase kebab-case filenames for optional documents. Preserve the four uppercase core filenames exactly.
- Instructions must use `type: instruction`, a domain-and-activation `description`, and `scope: repository` frontmatter.
- Playbooks must use `type: playbook` and a concise activation-oriented `description` frontmatter.
- Personas must use `type: persona` and a specialist-perspective-and-activation `description` frontmatter.
- Instructions must cover stable broad domains, never individual pages, routes, endpoints, features, or components.
- Knowledge must state when it applies and cite repository evidence for non-obvious claims.
- Prefer links to canonical code, configuration, ADRs, or documentation over copied implementation.
- Inside `.ai/`, use normal relative Markdown links with an activation phrase. Reserve `@path` imports for the root `CLAUDE.md` adapter.
- Cite implementation evidence with repository-root paths in backticks. Do not add secrets, personal data, private machine paths, speculative history, empty headings, or `TBD` sections.

## Creation and structural changes

Before adding, moving, merging, splitting, or removing a document:

1. Audit the relevant repository sources and read every active `.ai/` document completely.
2. Inventory the actual tree and compare it with `SUMMARY.md`, `BASE.md` routing, current domains, workflows, and risks.
3. Classify every existing document as **keep**, **update**, **merge**, **split**, or **remove**, with evidence.
4. Apply the evidence gate to every proposed document and identify its distinct activation condition.
5. Prefer a targeted update when activation and ownership still fit. Merge documents normally loaded together when their authoritative content overlaps; split only when activation conditions materially differ.
6. Obtain explicit approval for the exact optional-document structure before implementing a structural migration unless the user has already approved it or requested single-pass execution.
7. Update all inbound and outbound links and update `SUMMARY.md` in the same change.

When an existing `.ai/` system is audited, a full-system re-audit is mandatory: detect stale claims, broken paths, weak activation, duplicate authority, generic filler, routing drift, missing domains, and missing specialist lenses. Close every approved gap; prior existence or approval does not establish current completeness.

## Routing and orphan prevention

- `BASE.md` must keep routing compact and point prominently to `SUMMARY.md`; it must not reproduce the exhaustive catalog.
- `SUMMARY.md` must list every other active `.ai/` Markdown document exactly once, grouped by category, with a valid relative link and exactly one concise purpose-plus-activation sentence per entry.
- `SUMMARY.md` must not link to itself or repeat rules, procedures, evidence, or checklists.
- Every active document other than `SUMMARY.md` must have one catalog entry and a meaningful inbound route.
- Keep routing shallow and avoid circular reference-only chains.
- A create, move, rename, merge, split, or removal is incomplete until catalog entries and all references are reconciled.

## Authority, duplication, and drift

- Give each rule or fact one authoritative home and link to it elsewhere.
- Knowledge must not restate canonical architecture documentation merely for convenience; it should capture the material fact, constraint, or verified discrepancy that the canonical source omits.
- Playbooks must link to Instructions instead of duplicating their rules. Personas must link to Instructions and Playbooks instead of restating them.
- Intentional semantic overlap is allowed only for a verified externally managed root block retained for compatibility; document the exception and its owner.
- After an architecture, workflow, interface, security boundary, deployment, or design-contract change, update the owning `.ai/` document and re-check adjacent activation routes. Update `SUMMARY.md` only when its indexed sentence or tree changes.
- Remove obsolete content only after preserving still-valid guidance in the correct authoritative document and updating every reference.

## Historical snapshots

`.ai/.backup/` is conditional historical recovery storage, not active context.

- Immediately before replacing a substantive tracked root `AGENTS.md` or `CLAUDE.md`, save its exact pre-migration bytes as `.ai/.backup/AGENTS.md.original` or `.ai/.backup/CLAUDE.md.original`.
- Before removing or replacing a tracked legacy style contract, save its exact bytes under its original capitalization plus `.original`, then use it as the primary verified starting point for `STYLE.md`.
- Do not snapshot an adapter that already equals the minimal final adapter. Do not snapshot ignored, local, personal, or untracked instruction files.
- Never overwrite a snapshot. If the target exists, compare it with the current source, report any difference, and ask before creating an additional version.
- Snapshot files must remain byte-for-byte recovery artifacts. Do not translate, normalize, edit, load, route, catalog, or treat them as authority.
- Exclude `.ai/.backup/` from `SUMMARY.md`, orphan analysis, and ordinary context loading. Retain snapshots unless an explicitly approved cleanup says otherwise.

## Tool-managed root blocks

Treat comment-delimited instruction regions as externally managed until proven otherwise.

1. Preserve the complete original root file in the eligible exact snapshot before changing it.
2. Identify the owning package, generator, or framework from repository evidence and verify whether it requires the wording, markers, or root location.
3. Migrate the block's still-valid semantic guidance into the applicable active `.ai/` document without claiming ownership of the external wording.
4. If the owner requires the root location, place the minimal adapter first and retain the complete block byte-for-byte after it as the sole adapter exception.
5. If the owner does not require the root location, remove the active copy only after semantic migration and verification.
6. If ownership or location requirements remain uncertain, do not remove, rewrite, or relocate the block; report the evidence and ask.

Never edit inside a retained managed block or normalize its markers. Record any required semantic overlap as a compatibility exception.

## Maintenance procedure

1. Read this manual, `BASE.md`, `SUMMARY.md`, and every active `.ai/` document.
2. Audit current authoritative repository sources, working-tree state, tracked/ignored/untracked instruction surfaces, and managed blocks.
3. Classify all documents and apply the evidence gate to gaps and proposed changes.
4. Present structural changes for approval when required.
5. Create any eligible non-overwriting snapshots immediately before adapter or legacy-style replacement.
6. Make the smallest coherent edits, preserving one authoritative home per rule.
7. Rebuild `SUMMARY.md`, validate every relative link and frontmatter contract, check for orphaned or duplicate content, and run `git diff --check`.
8. Inspect the final diff for scope, snapshot exactness, stale references, private data, and unintended product-file changes.

The default root adapters are exactly `Read and follow @.ai/BASE.md completely before doing any work.` in `AGENTS.md` and `@.ai/BASE.md` in `CLAUDE.md`, except for a verified retained managed-block compatibility exception.
