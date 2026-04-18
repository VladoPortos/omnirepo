# OmniRepo Improvement Opportunities

This document is intentionally separate from `issues.md`.

- `issues.md` focuses on bugs, security risks, and correctness gaps.
- This file focuses on product direction, architectural improvements, logical gaps, and UI/UX opportunities for a stronger `v2.0`.

The recommendations below try to avoid repeating features that already exist in OmniRepo today.

## Framing

OmniRepo already covers a strong `v1.0` baseline:

- Multiple repository types and protocols
- Project-scoped access control
- User and project API keys
- Search, dashboard, audit log, maintenance mode
- Trivy integration
- Admin flows for TLS, trash, GC, and settings
- Web UI for common repository and project workflows

That means the next step is not “add random features”. The next step is to make the product:

- more predictable to operate
- more opinionated for enterprise workflows
- easier to understand at a glance
- safer for admins under pressure
- more polished for daily use

## Highest-Value Product Improvements

### 1. Promotion and Release Flow as a First-Class Feature

OmniRepo already has some sync and promotion-related capabilities, but a corporate environment usually needs a more explicit release pipeline model.

Recommended direction:

- Add named lifecycle stages such as `dev`, `qa`, `staging`, `prod`
- Let projects define allowed promotion paths between repositories
- Require comments or change reasons on promotion to higher environments
- Record promotion events in a human-friendly audit trail
- Show “where this artifact came from” and “where it was promoted to”

Why this matters:

- Corporations usually care less about raw storage and more about controlled movement of artifacts
- A visible promotion chain reduces mistakes and makes audits much easier

Difficulty:

- Medium if built on top of current repo/project concepts
- Higher if you want immutable release bundles, approval steps, and rollback support

### 2. Retention, Quotas, and Capacity Management

For corporate use, storage growth becomes an operational problem very quickly.

Recommended additions:

- Per-project storage quota
- Per-repository retention policies
- Keep-last-`N` versions rules
- Age-based cleanup rules
- Preview mode for cleanup policies before enforcing them
- Storage growth charts and “top consumers” views

Why this matters:

- Enterprises need predictable capacity planning
- Manual cleanup does not scale
- Storage ownership by project helps internal cost accountability

Difficulty:

- Easy to medium for quotas and reporting
- Medium for safe policy execution and preview simulation

### 3. Better Operational Visibility

The app should become much more self-explanatory for operators.

Recommended additions:

- A dedicated system health page
- Disk usage and free space warnings
- Database size and growth trend
- Queue depth and background job status
- Trivy DB freshness indicator
- Certificate expiry summary
- Repository sync freshness summary
- Long-running task history with duration and failure reason

Why this matters:

- Corporate operators need “what is unhealthy right now?” without checking logs
- This reduces support burden and makes the product feel production-ready

Difficulty:

- Mostly easy to medium
- High-value because the backend likely already has most of the raw data

### 4. Safer Destructive Actions

Enterprise users care about controlled damage more than speed.

Recommended additions:

- Explicit dry-run mode for cleanup, delete, and GC-style operations
- Better impact previews before deletion
- Soft locks / delete protection on important repositories
- Optional approval flow for destructive admin actions
- “Reason for change” fields for sensitive operations

Why this matters:

- Many enterprise incidents are operator mistakes, not code failures
- Safety rails are a competitive feature in internal platforms

Difficulty:

- Easy for previews and protection toggles
- Medium to high for approval workflows

## Logical and Architectural Improvements

These are not necessarily bugs by themselves, but they are places where `v2.0` could become much cleaner and easier to evolve.

### 1. Make Policies First-Class Instead of Scattered Flags

Today, early-stage products often accumulate policy decisions across handlers, settings, and repo-specific code.

Recommended direction:

- Introduce a policy layer for visibility, delete permissions, retention, public access, promotion rules, and scan requirements
- Centralize policy evaluation instead of repeating access/business logic across endpoints

Why this matters:

- It reduces divergence between UI behavior and API behavior
- It makes future enterprise features much easier to add

### 2. Standardize Background Jobs

The app already has job-like behavior. That should become a clear product primitive.

Recommended direction:

- One consistent job model for syncs, garbage collection, scans, imports, exports, and maintenance tasks
- Uniform statuses such as `queued`, `running`, `retrying`, `failed`, `completed`, `cancelled`
- Retry policy visibility
- User-facing logs for each job

Why this matters:

- It makes the system easier to reason about
- It gives the UI a single way to present backend activity

### 3. Unify Repository Capability Metadata

Different repository types have different operations, but the UI and API should not need hardcoded branching everywhere.

Recommended direction:

- Define capability metadata per repo type
- Examples: supports push, supports proxy/sync, supports scanning, supports browse, supports metadata edit, supports promotion

Why this matters:

- It simplifies UI rendering
- It avoids “special-case sprawl” as more repo types and actions are added

### 4. Separate “Artifact Registry” Concerns from “Admin Platform” Concerns

`v1.0` products often mix artifact operations, system administration, and operational troubleshooting into the same flows.

Recommended direction:

- Distinguish clearly between:
  - daily user workflows
  - project admin workflows
  - platform admin workflows
- Reduce cross-contamination between operator-only controls and routine package/repo usage

Why this matters:

- Enterprise users need role-appropriate interfaces
- It lowers accidental misuse and cognitive load

### 5. Build for Auditability as a Feature, Not a Side Effect

You already have audit logging. `v2.0` could make it much more useful.

Recommended additions:

- Rich audit entries with before/after values for settings changes
- Correlation between API action, job execution, and final result
- Filters by project, repo, actor, action type, and date
- Exportable audit reports
- Clear distinction between user actions and system actions

Why this matters:

- In corporate environments, audit trails are often reviewed by someone who did not perform the action
- Raw logs are not enough; explanation and context matter

## Corporate-Oriented Features Worth Considering

The user explicitly said not to focus on LDAP, remote users, or secret vault integration right now. The list below avoids those as primary drivers.

### 1. Compliance and Hygiene Reporting

Add built-in reports for:

- Public repositories
- Repositories without recent activity
- Projects nearing quota limits
- Expiring certificates
- Stale API keys
- Repositories with failing syncs
- Trivy DB out-of-date status
- Artifacts or repos without recent scans

Why this matters:

- Internal platform teams regularly need this kind of hygiene reporting
- It turns OmniRepo into a system that helps administrators govern the environment

### 2. Air-Gapped and Controlled Transfer Workflows

Even without full remote replication, many corporate setups need controlled artifact movement.

Recommended additions:

- Export/import bundles for selected artifacts
- Signed transfer manifests
- Offline promotion packages
- Integrity verification before import

Why this matters:

- This is common in segmented or high-control environments
- It fits the “corporate artifactory replacement” direction well

### 3. Immutable Release Controls

Recommended additions:

- Mark repositories or paths as immutable after release
- Prevent overwrite of protected versions or tags
- Optional legal-hold / retention exemption markers
- Stronger distinction between snapshot and release repos

Why this matters:

- Enterprises often require confidence that released artifacts cannot silently change

### 4. Notification Hooks

Recommended additions:

- Email or webhook notifications for:
  - failed syncs
  - failed scans
  - expiring TLS certificates
  - quota thresholds
  - maintenance mode activation
  - destructive admin actions

Why this matters:

- Operators should not need to keep the UI open to stay aware of problems

### 5. Better Backup and Recovery UX

Recommended additions:

- Backup status page
- Restore guidance in UI
- Configuration export summary
- Recovery checklist documentation generated from live config
- “Last successful backup” display

Why this matters:

- Enterprise trust depends heavily on recovery clarity, not just backup existence

## Easy Wins

These are relatively small improvements with high user-facing value.

### 1. Client Configuration Snippets Everywhere

For each repository page, add copyable examples for:

- `docker login`, `pull`, `push`
- `pip` / `.pypirc`
- APT source lines
- RPM repo config snippets
- Helm repo add / push / pull
- Git clone / fetch URLs
- S3 CLI and SDK examples

Why this matters:

- This reduces friction immediately
- It is one of the most useful features in internal tooling

### 2. Better Empty States

Instead of blank screens, show context-aware next steps:

- Create first repo
- Add members to a project
- Upload first artifact
- Enable scanning
- Configure TLS

Why this matters:

- Early-stage products often feel “unfinished” mainly because empty states are silent

### 3. Saved Filters and Views

Recommended additions:

- Save common searches
- Save table filters
- Pin favorite projects or repos
- Recently visited items

Why this matters:

- Daily users repeat the same navigational work constantly

### 4. Command Palette / Quick Action Launcher

Examples:

- Jump to project/repo
- Create repo
- Create API key
- Start maintenance mode
- Open audit log
- Open scan results

Why this matters:

- This is a modern UI feature with strong usability payoff

### 5. Better Failure Messaging

Recommended additions:

- Replace vague failures with actionable guidance
- Differentiate validation problems, permission problems, transient backend failures, and operator-action-required failures

Why this matters:

- Internal users lose confidence quickly when the system “just errors”

## Harder `v2.0` Investments

These are larger bets that could meaningfully raise the product ceiling.

### 1. Stronger Multi-Node / HA Story

Even if not needed immediately, corporate buyers usually ask early whether the system can grow beyond a single-node setup.

Possible direction:

- Move toward a cleaner split between metadata plane and storage plane
- Clarify what would be required for HA-safe jobs, locking, and shared storage

Why this matters:

- Even if the feature is not implemented in `v2.0`, having an architecture path matters

### 2. Richer Artifact Metadata and Provenance

Recommended additions:

- SBOM attachment and browsing
- Provenance / attestation visibility
- Build source linkage
- Related artifacts / dependency graph views

Why this matters:

- Modern corporate environments increasingly want supply-chain visibility, not just storage

### 3. Policy Engine

Examples:

- Require scan pass before promotion
- Block public read on specific project classes
- Enforce naming conventions
- Require immutable mode on release repos
- Require retention policy on new repos

Why this matters:

- This turns OmniRepo from a storage service into a platform with enforceable governance

### 4. Bulk Administration

Recommended additions:

- Bulk repo edits
- Bulk project membership edits
- Bulk API key review/revoke
- Bulk retention changes
- Bulk artifact cleanup actions

Why this matters:

- Corporate environments generate admin volume
- Single-item forms do not scale operationally

## UI Improvements

The current UI appears functional and broad in scope. For `v2.0`, the biggest improvement is not “more pages”; it is stronger visual hierarchy, faster navigation, and more actionable pages.

### 1. Stronger Dashboard Design

The dashboard should answer these questions immediately:

- Is the system healthy?
- What changed recently?
- Which projects or repos need attention?
- Are syncs, scans, or storage nearing trouble?

Recommended additions:

- Health summary cards
- Recent failures panel
- Storage growth panel
- Expiring certs / stale scan DB warnings
- Queue and job summary
- Recent audit highlights

### 2. Denser Enterprise Tables

Corporate users spend a lot of time in tables. Invest there.

Recommended additions:

- Column chooser
- Saved column layouts
- Bulk select and bulk actions
- Better sort/filter combinations
- Sticky headers
- Inline status badges
- Export current view to CSV

Why this matters:

- This often matters more than flashy visuals in admin-heavy products

### 3. Better Repository Overview Pages

A repo page should feel like a control center, not just a settings/details page.

Recommended content:

- Quick usage commands
- Latest artifacts
- Recent uploads
- Sync status
- Scan status summary
- Retention / immutability / visibility policy summary
- Last modified actors

### 4. Visual Language for State and Severity

Recommended improvements:

- Consistent color semantics for healthy / warning / failure / disabled / maintenance
- Better severity treatment for scan findings
- Clear badges for public/private, immutable, stale, syncing, failed, protected

Why this matters:

- Users should understand risk and state before reading details

### 5. Better Navigation Model

Recommended additions:

- Global search always available
- Pinned/favorite projects and repos
- Recent items
- Breadcrumbs everywhere
- Context-switch shortcuts between artifact view, repo settings, project settings, audit, and jobs

### 6. More Helpful Detail Drawers and Side Panels

Instead of forcing full page switches for every item:

- Show artifact detail drawer
- Show scan result drawer
- Show member details drawer
- Show audit event detail drawer

Why this matters:

- It speeds up investigative workflows

### 7. Progressive Disclosure for Complexity

Early-stage admin products often either hide too much or show too much.

Recommended pattern:

- Keep common actions obvious
- Collapse advanced protocol-specific options behind expandable sections
- Add inline explanations where terms are domain-specific

### 8. Better Loading, Error, and Empty States

Recommended improvements:

- Skeleton states instead of blank loading screens
- Retry actions on recoverable errors
- Clear inline validation
- Helpful “what to do next” empty states

These are small polish items, but they strongly affect perceived quality.

## Small Graphical and UX Polish Ideas

These are lower-cost improvements that can make the product feel much more deliberate.

- Use stronger spacing and typography hierarchy on admin pages
- Add compact and comfortable density modes for tables
- Standardize card layouts and section headers
- Use subtle timeline views for audit and job history
- Add inline icons only where they improve recognition, not everywhere
- Improve button priority so destructive and primary actions are visually distinct
- Add copy-to-clipboard affordances for URLs, commands, digests, and keys
- Make status badges consistent in shape, wording, and color
- Ensure responsive behavior for laptop-sized screens used by admins

## Suggested Prioritization

If you want a practical sequence for `v2.0`, this would be a strong order:

### Phase 1: Immediate Product Polish

- Client configuration snippets
- Better empty states
- Health/status dashboard
- Better failure messaging
- Saved filters and favorites
- Better repo overview pages

### Phase 2: Operational Maturity

- Retention and quotas
- Job model standardization
- Hygiene/compliance reporting
- Notification hooks
- Safer destructive workflows

### Phase 3: Enterprise Differentiation

- Promotion and release pipeline model
- Immutable release controls
- Audit trail enrichment
- Air-gapped export/import bundles
- Policy engine

### Phase 4: Strategic Platform Evolution

- HA direction
- Provenance / SBOM / attestation UX
- More advanced governance and bulk administration

## Final View

For `v1.0`, OmniRepo already appears to have useful protocol coverage and a meaningful administrative surface.

The most valuable next step is not adding niche integrations. It is making the platform:

- easier to operate
- safer to trust
- faster to navigate
- clearer under failure
- more structured around enterprise release and governance workflows

If you want, the next step after this document can be a stricter prioritization pass:

- `easy / medium / hard`
- `high ROI / medium ROI / low ROI`
- or a concrete `v1.1`, `v1.5`, `v2.0` roadmap draft
