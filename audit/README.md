# Shield fork audit

Audit baseline: upstream Shield `3f6313fa3f80dc568f20652bb93a6abb7ba3a506` (2026-07-09).
The last Shield tree verified identical to tg-spam is
`808353b26aa292d2a236ba314ea748fb0965251b` (2026-04-08). The manifests in this
directory make the complete path inventory and both deltas reproducible.

## Coverage

`classification.tsv` assigns every tracked or newly added path to one review class. Vendored source
is treated as third-party generated material: this audit verifies module checksums and reviews
`go.mod`, `go.sum`, and `vendor/modules.txt`; it does not falsely represent vendored code as a
first-party line-by-line audit. First-party source, executable configuration, CI, containers,
storage, Telegram actions, authorization, LLM egress/parsing, and retention receive semantic
review and tests.

`changed-since-tg-spam.tsv` is the rename-aware path delta from the last identical tg-spam tree.
`changed-since-tg-spam-numstat.tsv` and `changed-dirstat.txt` quantify that delta.
`implementation-status.txt` captures the staged implementation delta when the inventory is
generated. `paths-head.txt` is the complete staged path set and `summary.txt` records the exact
baseline. `implementation_tree` is the input tree used to generate the manifests; by design it
precedes restaging the generated manifest bytes themselves, avoiding a self-referential tree hash.

## High-risk findings addressed

- privileged container publishing could build a fork PR SHA; publishing now runs only on trusted
  pushes to `master` or version tags;
- CI used floating action tags, a floating linter, and the invalid `paths_ignored` key; workflow
  actions are pinned to verified commit hashes, tool versions are fixed, and path filtering is
  valid GitHub Actions syntax;
- admin/report callbacks trusted membership in the admin chat instead of the actor; both paths now
  require a current super-user identity;
- LLM responses allowed JSON repair, wrappers, regex extraction, missing fields, and low-confidence
  sanctions; both text and vision paths now use strict single-object parsing and code-level
  confidence gates;
- CAS sent Telegram user IDs externally by default; it is now opt-in;
- unfinished ingress rows could be suppressed forever after a crash; the supported single poller
  now reclaims unfinished rows on Telegram redelivery;
- retention used non-existent timestamp columns and swallowed SQL failures; columns now match the
  real schemas and failures propagate;
- queue close could race a publisher and panic; close now wakes publishers before closing the
  stream under the write lock;
- the main moderation pipeline and vision debug logs exposed full updates and image prefixes; those
  paths now retain only bounded metadata, sizes, and hashes. Existing opt-in debug/admin diagnostics
  elsewhere in upstream can still contain message text and must not be enabled in production;
- concurrent tenant onboarding admitted multiple successes; the onboarding transaction boundary is
  serialized within the service.
- the PostgreSQL Compose example shipped a fixed password; startup now requires an operator-provided
  `POSTGRES_PASSWORD`.
- `data/tg-spam.db.local` was a tracked local SQLite artifact containing 643 preset message samples;
  it was not required by the image conversion flow and has been removed from this fork. The source
  text corpora remain reviewable files rather than an opaque duplicate database.

## Verification boundary

SQLite tests, race tests, parser adversarial tests, topic/album state tests, callback authorization,
queue shutdown, and focused storage migrations run locally. PostgreSQL integration tests require a
Docker/rootless container provider; if unavailable, that environmental limitation must remain
visible in the final report and CI must execute the PostgreSQL suite before merge or deployment.
`golangci-lint` v2.12.2, including its configured `gosec` checks, runs clean against first-party
application, library, and audit-tool packages. `govulncheck`, `gitleaks`, `trivy`, `syft`, and the
repository's `unfuck-ai-comments` helper are not installed in this workspace; their absence is a
stated audit boundary, not a successful scanner result.

The production rollout remains shadow-first. No live Telegram action is authorized merely by this
audit; `COMMUNITY_APPLY_ACTIONS` is false unless an operator explicitly enables it after reviewing
shadow decisions.
