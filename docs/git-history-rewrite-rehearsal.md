# Git history rewrite: offline rehearsal (10 November 2026)

The task record for removing `v2/swhurl-v2`, the committed 11 MB binary, from this repository's history. This covers
approved rehearsal steps 1–3: inventory, external backups, and a rewrite in a separate clone that was then verified.
It also records a read-only look at what GitHub keeps and the separately approved Stage A Activity read experiment
in §9. Stage A does not authorise a production reader, deployment or history cutover. **No rewritten history was pushed, reconciled or changed live.** The history rewrite touched only scratch clones;
no `gc` ran in an existing repository.

Every artefact is in `/home/sam/history-rehearsals/20261110T214207Z/` (`$D` below): `scripts/`, `logs/`, `backup/`,
`rewrite/`, `check/`, `scratch/`. That is 421 MB in total.

## 1. Scope

- **Approved:** inventory of remote and local refs; external mirror and bundle backups with a restore test;
  `git-filter-repo --path v2/swhurl-v2 --invert-paths` in a fresh, independent mirror clone, keeping the SHA map;
  verification by object identity; size comparison; `make check` on the rewritten `main`; an inventory of SHA
  references in the tree; a read-only check of GitHub's pull-request refs and `merge_commit_sha`.
- **Not approved, and not done:** pushing anything, a force-push, any GitHub setting or support request, rewriting
  the main checkout or a worktree, editing docs or config to new SHAs, changing application code.

## 2. Inventory (step 1)

**Remote** (`git ls-remote`, `$D/logs/ls-remote.txt`): 151 lines. These are `HEAD` and `refs/heads/main` at
`77fa0904abccc75c5e16c81d4ba660aaaf550cbd`, 36 branches and 114 `refs/pull/*` (no tags). `main` moved from `8d5e467b`
to `77fa0904` during the session; another session pushed it, not this one.

**Local** (`$D/scripts/inventory-local.sh`, output in `$D/logs/inventory-local.txt`; `status` was run with
`--no-optional-locks`):

- **Main checkout:** `/home/sam/ghq/github.com/samclement/swhurl-platform`, on `main` at `77fa0904`, clean.
- **Refs:** 11 branches, including 7 `worktree/*`, `pr100-test` and `drill/recovery`; 12 `refs/m3/*`; 42
  `refs/remotes/origin/*`; no tags.
- **Stash:** 4 entries. Only `stash@{0}` is a ref; the other three live in the reflog.
- **Worktrees** (from `git worktree list`):

  | Worktree | Commit | State |
  | --- | --- | --- |
  | `.herdr/.../worktree-brave-cloud-7748` | `8d5e467b` | clean |
  | `.herdr/.../worktree-calm-meadow-83e9` | `14c145cc` | clean |
  | `.herdr/.../worktree-clear-field-2ed2` | `a00f888f` | **dirty:** `M docs/architecture.md`, `?? docs/operator-journeys.html` |
  | `.herdr/.../worktree-quiet-harbor-0165` | `60410859` | clean |
  | `.herdr/.../worktree-quiet-meadow-39ed` | `a00f888f` | clean |
  | `.herdr/.../worktree-rapid-cloud-468e` | `bc12aaed` | clean |
  | `.herdr/.../worktree-silver-valley-537f` (this one) | `8d5e467b` | clean before this report |
  | `/tmp/parity-new` | `7ac63363`, detached | **dirty:** 1 untracked file |
  | `/tmp/parity-old` | `7ac63363`, detached | **dirty:** 2 modified files |
  | `/tmp/pr100` | `c0182fd3`, detached | clean |
  | 11 other `/tmp/*` entries | — | prunable: the directory has no `.git` |

- **Other clones found** (`find` limited to `~/ghq` at depth 5, `~` at depth 3 and `/tmp` at depth 3, whose origin is
  this repository): `/tmp/producer-commit-check.{5YnOJ3,yq3Q6k}`, `/tmp/rv/clone`, `/tmp/rv/clone2` and
  `/tmp/ws-parity.3dGwig`. This is **not** a complete search of the machine, and other machines, CI caches and
  forks were not looked for.

**Where the binary is** (`$D/logs/commits-touching-path-all-refs.txt`, `path-blobs.txt`): 11 commits touch
`v2/swhurl-v2`. `ca776af5` added it on 26 October; `58b5fe62` deleted it and ignored it on 3 November. Along the way
there are 10 distinct blobs of 10.9–11.3 MB each, 38.9 MB on disk in the pack. 332 of the 2,538 commits across all
refs have the file in their tree. 565 commits are `ca776af5` or descend from it, and 11 branches and 24
`refs/pull/*` contain it.

## 3. Backups (step 2)

| Artefact | Content | Verification |
| --- | --- | --- |
| `$D/backup/swhurl-platform.mirror.git` | `git clone --mirror` over SSH: 150 refs including `refs/pull/*` | refs equal to `ls-remote`; `git fsck --full` rc 0 |
| `$D/backup/swhurl-platform-remote-all.bundle` (50,158,303 B, sha256 `3d8a31cc…50`) | `bundle create --all` from the mirror | `git bundle verify` rc 0, "complete history"; restored with `clone --mirror`, refs identical, `fsck --full` rc 0 |
| `$D/backup/swhurl-platform-local-all.bundle` (48,336,273 B, sha256 `912375ee…e3`) | `bundle create --all` read from the main checkout: branches, `refs/m3`, `refs/stash`, `refs/remotes` | `bundle verify` rc 0; restored, refs identical, `fsck --full` rc 0 |
| `$D/backup/swhurl-platform-local-stash-reflog.git` | the 4 stash commits as `refs/stash-entries/0..3` (fetched by SHA) | `fsck` ok |

These back up committed and stashed objects only. Uncommitted changes in the dirty worktrees above are **not** backed
up; they belong to their sessions.

## 4. Rewrite (step 3)

In `$D/rewrite/swhurl-platform.rewritten.git`, a `clone --mirror --no-local` of the remote bundle (refs checked equal
to the mirror before the run):

```
uvx --from git-filter-repo==2.47.0 git-filter-repo --path v2/swhurl-v2 --invert-paths
```

It finished with rc 0 in 0.66 s (`$D/logs/filter-repo.log`). The SHA map is kept in
`$D/rewrite/swhurl-platform.rewritten.git/filter-repo/` (`commit-map`, 2,538 rows; `ref-map`, 150 rows). Key
mappings:

| Old | New | What |
| --- | --- | --- |
| `77fa0904abccc75c5e16c81d4ba660aaaf550cbd` | `5d1bc1ed8097e89594ceff8aabf2122685e78a1f` | `main` |
| `ca776af5be21826b86a6344a8b6cb8c70e2ffe82` | `e114ceeb20699e2186d200f91e120a36143b205f` | binary added |
| `58b5fe62f8f7b964e009177cf31c95d549852179` | `eec3d236b9c1a69f478809d5e549249a1a593733` | binary removed |
| `83705c4d8ae4e4d3331b46c54d5f0a0d1c38f0af` | `a4aa980c2ebfdd01c4b4ace2da4213f485eeee85` | PR #101 merge |
| `ffc3e2ccfbcf40923da0b4bc6c24ff18f66ca2cc` | `19595e6cda0b15c9fea6036b5fd73b985f228393` | PR #102 merge |
| `aa13e550ade6d980d3e9d488926d75b2f0a1b05a` | `35ad15984aa6d3e22d1855f43f50fce8488a2231` | root (first changed commit) |

### Every commit SHA changed, not only those after the binary

**All 2,538 commits got new SHAs; none was dropped.** The cause is signatures. 73 commits carry a `gpgsig` header:
72 GitHub web-flow merges and the root commit, which has a personal signature. 53 of them, the root included, come
before `ca776af5`. `git-filter-repo` 2.47.0 runs `git fast-export` without keeping commit signatures (the source
passes only `--signed-tags=strip`), so the root's bytes change and every descendant follows.

The verifier checks the other possible causes. Encodings, authors and committers are byte-identical. 122 commit
messages had an abbreviated old SHA replaced by the new one; that is a consequence of the new SHAs, not a cause.

Whether this is avoidable: not with the pinned release run as approved. Keeping the signatures unchanged would at best
keep the commits before `ca776af5`. A newer filter-repo or a verbatim-signature mode was **not tried**, as it falls
outside the approved scope. Even then, at least 565 commits (`ca776af5` and its descendants) would change, and the 20
GitHub signatures among them could not stay valid.

### Verification

`$D/scripts/verify_rewrite.py` checks every row of `commit-map` by object identity (`$D/logs/verify-rewrite.log`,
rc 0):

- **Trees:** for 2,206 commits whose old tree lacks the path, the new tree id equals the old one. For the other 332,
  each tree on the path to the file keeps the same entries by object id, minus `swhurl-v2`. 0 differ otherwise.
- **Parents:** every commit's parents are its mapped old parents (0 mismatches; 0 dropped commits to account for).
- **Headers:** every header other than `tree`, `parent` and `gpgsig` is byte-identical (0 mismatches). 73 signatures
  were removed.
- **Messages:** 2,416 are identical. 122 differ only where an abbreviated old SHA became the new one. 0 differ
  otherwise.
- **Refs:** all 150 match `ref-map` and the rewritten repository, and none exists on only one side.
- **The check can fail:** run with a path that never existed, it fails (rc 1, 332 `tree_bad`;
  `verify-rewrite-negative.log`).

**The binary is unreachable** (`$D/logs/blob-reachability.log`): none of the 10 blobs is in the rewritten object
store (`cat-file -e`), 0 of the 29,309 objects reachable from all refs is one of them, and no commit touches the path.
The largest blob left is `docs/current-state.md` at 844 kB. There are no tags.

**Sizes** (`$D/logs/sizes.log`, `archive-sizes.log`). These are local measurements, not a promise about GitHub's size:

| Measure | Before | After |
| --- | --- | --- |
| pack as written (`size-pack`) | 49,776 KiB | 11,796 KiB |
| both repacked identically (`repack -a -d -f --window=250 --depth=50`) | 46,397 KiB | 8,825 KiB |
| bundle of `refs/heads` only | 46,675,334 B | 8,213,786 B |
| `git archive \| gzip -n -6`, PR #101 merge | 8,299,847 B | 2,220,731 B |
| same, PR #102 merge | 8,299,839 B | 2,220,708 B |
| same, `main` | 2,478,174 B | 2,478,176 B (same tree; the tar header holds the commit id) |

**`make check`** in `$D/check/swhurl-platform`, a clone of the rewritten `main` (`5d1bc1ed`, clean, no
`v2/swhurl-v2`):

```
pi-durable-subagents hold machine -- bash $D/scripts/run-check.sh $D/check/swhurl-platform
```

It passed (rc 0, 35.6 s; `$D/logs/make-check.log`): ruff, shellcheck, the 299 Python tests, the 98 codegraph tests,
`check-docs` against the rewritten Git, the OTel collector fixtures (with three deprecation warnings about component
names) and 32 render entrypoints. None of what `make check` runs reaches a cluster: `kubectl kustomize` renders
locally, and the script set `KUBECONFIG` to a file that does not exist and unset the GitHub tokens. Chart and tool
downloads are the only network use.

## 5. What GitHub keeps (read-only)

From the API (`gh api .../pulls?state=all`, `$D/logs/pulls-classified.tsv`), the mirror's `refs/pull/*` and
`gh api .../git/trees`:

- **Activity's two binary-era changes:**
  - PR #101 (`console/v2-scale-swhurl-try-10-staging-8bcfd64`, merged 2026-10-31T23:18Z, `merge_commit_sha 83705c4d…`).
  - PR #102 (`…-83705c4`, merged 2026-11-01T00:19Z, `ffc3e2cc…`).

  GitHub's tree API shows `v2/swhurl-v2` as blob `ad4d9bb3…` of 11,299,470 bytes in both. Their local archives are
  the 8.3 MB that `docs/current-state.md`'s top entry measured. These are swhurl-try-10/staging's only merged PRs in
  that window, and the branch names are the console's scale jobs. That they are jobs 41 and 42 is **inferred**: the
  `console/console-jobs` record was not read.
- **The console keeps reading the old SHA:** `v2/cmd/swhurl-v2/console_activity.go:354` sets
  `Commit = MergeCommit`, read from `merge_commit_sha` (`v2/adapters/github/console.go:351`), and
  `orchestration.mergeTrees` (`v2/orchestration/console_changes.go`) fetches `/tarball/<that SHA>`. A rewrite
  changes no pull request's `merge_commit_sha`, so Activity would go on asking for `83705c4d` and `ffc3e2cc`.
- **`refs/pull/*` keep the old commits reachable.**
  - 24 of the 114 refs contain `ca776af5`, and so reach the binary: heads and merges of #47, #60, #95, #97, #98 and
    #99, the merge of #104, and heads #100–#110 (`$D/logs/pull-refs-reaching-binary.txt`).
  - Five open PRs (#47, #60, #97, #98, #99) have a test merge whose own tree holds the binary.
  - Both Activity merge commits are ancestors of `refs/pull/{95,103..110}/head`, `refs/pull/95/merge` and
    `refs/pull/104/merge`, so they stay reachable through those refs after any branch force-push.
  - GitHub documents these refs as read-only; that was **not tested by a push**.
- **GitHub serves unreferenced commits by SHA:** `git fetch --depth=1` of `7b31ad44…` (PR #110) and `1caef5a1…`
  (PR #103) succeeded. These are closed-PR test merges under no advertised ref (`$D/logs/unref-fetch.txt`). When, or
  whether, GitHub stops serving old objects after a force-push was not observed and is not ours to control.

## 6. SHA references in the tree

`$D/scripts/sha_refs.py` ran over `main`'s tree (`77fa0904`). The full list is `$D/logs/sha-refs.tsv`, the summary
`sha-refs-summary.txt`. Hex ids that resolve to a commit here (a short id can do so by coincidence):

- **Docs:** 1,275, of which 1,076 are in `docs/current-state.md` and 172 in `docs/plan.md`, plus a few each in
  `visual-change-tool.md`, `iteration-2-plan.md`, `v2-deletion-plan.md`, `architecture-review.md`, `apps.md` and
  `model-review.html`. Evidence links like these become old SHAs that are absent from the rewritten branches.
- **Generated config:** `"revision": "6276a608…"` in `platform/egress/allocation.json` and `v2-records/calls.json`,
  which `make v2-render` writes. `make check` passed with them unchanged.
- **Tests and fixtures:** `b63d2aff…`, used as a console image tag in `tests/fakecluster.py`,
  `v2/cmd/swhurl-v2/console_test.go`, `testdata/platform.html` and `observe/platform_scope_test.go`; `1edf3705`,
  repeated as a fake revision; and `913046e5`, `c801f707`, `9766d4c5`, `25dbe522`, `006f72c0`, `a755d200` and
  `334a0f11`, used as provenance notes in tests, testdata READMEs and `devtools/codegraph/README.md`.
- **Not affected by the rewrite:**
  - 1,466 hex tokens that resolve to no commit here: app and template commits, `src-<hash>` image tags, and others.
  - 1,051 `sha256:` digests: images, `uv.lock`, charts.

Nothing was edited. Whether to map the docs' references through `commit-map` is a cutover decision.

## 7. Corrections to earlier statements

- **Commit count:** "538 commits rewritten" is wrong for this rewrite. All 2,538 changed. At least 565 must change
  even if signatures were kept, and that count grows with every commit.
- **Sizes:** the measured figures are in §4. GitHub's own repository size and its tarball sizes after a push were not
  measured and cannot be promised.
- **Reversibility:** the backups in §3 restore every ref and stash commit, as the restore test shows. What a
  force-push makes hard to undo is other clones, open PRs and evidence links, not the data.
- **Purge:** a force-push alone is not a guaranteed purge. `refs/pull/*`, `merge_commit_sha` and GitHub's
  fetch-by-SHA keep the old commits available, as shown in §5.

## 8. Cutover gate (not approved)

Stop conditions met in this rehearsal: none of backup, rewrite or check failed. The cutover stays **blocked** on these
decisions:

1. **Activity:** after a rewrite, Activity's mapping from a change to its tree would still fetch the binary-era
   archives of PRs #101 and #102 through `merge_commit_sha`. A rewrite does not fix that read. That needs its own
   approved change: mapping through `commit-map`, or deciding those rows another way.
2. **Pull-request refs:** what to do about `refs/pull/*` and the 5 open PRs that reach the binary. Either accept that
   they remain or ask GitHub Support, an outward action that needs separate approval.
3. **Signatures:** accept losing the 73 signatures and getting a new SHA for every commit, or approve trying a
   signature-preserving tool first.
4. **Before any force-push:** freeze `main` (it moved during this session); re-run steps 1–3 on the then-current
   remote; decide about the docs' SHA references (§6); plan for the other worktrees, clones and their dirty work
   (§2), which would all need re-cloning or rebasing; and plan Flux's `GitRepository` and its reconcile, under the
   AGENTS.md confirm-first rule.

## 9. Stage A: Activity read experiment (10 November 2026)

**Decision: go for a targeted-reader implementation proposal; no-go for replacing the entire console bundle with
individual blob reads.** Stage B was then approved and implemented offline ([§10](#10-stage-b-the-targeted-reader-implemented-offline-10-november-2026)); deployment still requires the operator's confirmation. The running console,
Git history, image pins and Flux configuration are unchanged.

### Scope and method

The operator approved Stage A only: prove whether historical progress evidence can be read faster without changing
production code. The standalone Go module, fixtures, scripts and raw measurements are in
`/home/sam/history-rehearsals/20261110T214207Z/activity-stage-a/`. Its `go.mod` imports the task branch's unchanged
v2 implementation through a local replacement. No experiment code is under `v2/`, no API baseline changed, and no
production bundle/cache is replaced. The branch was rebased onto `origin/main` at `77fa0904` before the experiment;
the uncommitted rehearsal report was preserved.

Three samples per method per original merge commit (#101 `83705c4d`, #102 `ffc3e2cc`) were measured from this host.
Each sample gets a fresh HTTP transport, no application cache, a 20-second overall context, and includes
`ConsoleRenders` processing. Only authenticated HTTP GETs were made to GitHub and its archive redirect; no cluster
credentials, cluster requests, writes, pushes or reconciles were used. The test token is passed through the
environment, never logged. Each measurement ran under the machine lease. The two runs made 510 HTTP GET requests
in total, including archive redirects.

The candidates are:

1. **Archive:** the existing GitHub adapter's tarball, production `FilterTarball`, then `ConsoleRenders`.
2. **Selective bundle:** a complete recursive tree listing at the exact commit, then only the regular-file blobs
   selected by the current console's rules. Identical blob SHAs are fetched once, with at most four blob reads in
   flight. This selector is copied into the external experiment only, not a proposed second production authority.
3. **Selective unit:** the same complete tree listing, but only the four paths `ConsoleRenders` consumes for
   `swhurl-try-10/staging`: its app, instance and release records and committed HelmRelease. Nothing else is fetched.

Each blob is read by immutable SHA and its Git identity and declared size are checked. Missing paths are accepted
only after a complete tree listing. Truncation, malformed listings, HTTP failures, corrupt blobs and cancellation
return an error without a partial result. Symlinks and submodules are not followed. Both candidates avoid fetching
the binary blob; the archive baseline deliberately downloads it for comparison.

### Results

Six samples per method (three per commit), with no failed measurement:

| Method | Median | Range | HTTP requests per cold read | Response-body bytes | Selected files |
| --- | --- | --- | --- | --- | --- |
| Archive | 14.358 s | 14.157–15.428 s | 2 (API + redirect) | 8,299,639–8,299,961 | 103 |
| Selective bundle | 5.677 s | 5.525–6.093 s | 78 | 426,641 | 103 |
| Selective unit | 0.717 s | 0.639–0.800 s | 5 (tree + four blobs) | 345,465 | 4 |

Response-body bytes are counted after HTTP content decoding, not TCP/TLS wire traffic. The unit's four files total
4,738 bytes; most of the candidate's response-body data is the tree listing. The whole selected bundle totals
120,285 bytes. The targeted read is about 20 times faster than the archive in these samples, without relying on a
history rewrite. The full-bundle candidate is faster too, but its 78 requests are an unnecessary API-budget cost
for one change. This replaces the earlier estimate based on 139 current-tree files with measurements of the actual
historical trees (103 files, 77 distinct blobs).

For all six selective-bundle samples, the full selected file maps and `ConsoleRenders` outputs equal the archive
baseline. For all six selective-unit samples, all four selected files and the requested unit's complete
`pages.Render` value equal the baseline projection. This is evidence parity, not a deployed HTML/progress test.

### Verification and artefacts

- `go test -race -count=1 -v ./...` passed in the external module. Fixtures cover archive/render parity, each of the
  four inputs missing, truncated/malformed/duplicate/traversing tree entries, oversized selected files, skipping
  links/submodules, 403/404/429/500 and corrupt blob failures, cancellation, immutable-revision validation, blob
  deduplication, and four-request concurrency with six unique blobs.
- `go vet ./...` passed in the external module; repository `make check-docs` passed after the report update.
- `python3 summarize.py` passed: exactly 18 samples, all parity assertions true, no reported measurement errors.
- `tests.log`, `measurements-first.jsonl`, `measurements-repeat.jsonl`, their stderr files, `summary.txt`, and the
  complete Go source are retained beside the experiment. The initial test launch failed because the lease CLI was
  absent from this shell's PATH; subsequent runs used its absolute Node entry point and passed.
- No production source changed, so the repository's `make check` and `make check-v2` were not re-run for Stage A.
  Their earlier rehearsal results in §4 apply only to that rehearsal. The v2 status page and workstream registry
  remain unchanged: this experiment does not deliver a new production capability or move the deployed-console gate.

### Limits and next gate

These are cold local-client evidence-read timings, not end-to-end Activity page timings. GitHub/CDN caches are
outside our control; sample order was archive, full bundle, targeted unit, not randomised. No server/pod restart,
page render, cluster observation, API rate-limit exhaustion, or concurrent user load was exercised. Only the two
problematic commits and one unit were measured. The four-file result does not supply a complete console tree, and
must never be inserted into the existing full-tree cache as if it did.

The next slice, since approved and implemented offline in §10: explicitly request a unit at a pinned
commit for Activity/job progress, reuse one production path-selection authority, preserve the existing render
semantics, and bound/cache evidence under the correct commit-and-unit identity. Keep ordinary page bundle reads
and the one-cold-history-read-per-page protection unchanged initially. Tests must cover multiple units sharing a
commit, absent/failed evidence, cache isolation and retry, plus the complete Activity/job page paths. Deploy and
verify a cold real page only after that implementation is approved and validated; no subsecond whole-page promise
is justified by this experiment. History cleanup remains a separate maintenance decision (§8), not a prerequisite
for the measured improvement.

## 10. Stage B: the targeted reader, implemented offline (10 November 2026)

**Approved by the operator for implementation and local validation, then separately for rollout.** This section is
the offline validation: no request reached the cluster or GitHub, and no credential was used or changed. It was
built on `77fa0904`. The behaviour is described on its canonical page,
[read performance](console.md#read-performance), and the dated record is in
[current state](current-state.md#activity-reads-a-merge-commits-four-files-not-its-archive-10-november-2026).

### What changed

- **Adapter** (`v2/adapters/github/console_files.go`): `Console.CommitFiles(repository, token, commit, paths)`.
  It reads the complete recursive tree listing at an immutable commit, then only the requested regular-file blobs
  (mode `100644`/`100755`), each blob id once and at most four at a time. Each blob is checked against its Git id
  and the listing's size, with a 1 MiB limit per file. A missing `truncated` flag, a missing tree, a bad id, an
  unsafe or duplicate path, or a requested file with no size or too large is refused before any blob is read. The
  first failed blob cancels the rest, and nothing partial is answered. Links and submodules are absent, as in
  `FilterTarball`. Errors carry fixed reasons only. There is no archive or `main` fallback.
- **Fake and contract** (`githubtest/console_fake.go`): `ConsoleFake.CommitFiles`, with `History` for files at
  older commits; an unknown commit is an error. The shared contract checks a found file, an absent path, an unknown
  commit, a moving name and a path that leaves the tree, for the real adapter's scripted server and the fake.
- **Orchestration** (`v2/orchestration/console_evidence.go`): `ConsoleSource.Files`, `ConsoleSourceOf.Files` and
  `ConsoleEvidence`. `ConsoleEvidence` owns the four paths a unit's render takes and builds the render with the
  unchanged `ConsoleRenders`. It keeps renders by (commit, unit), at most 240, oldest first, proved absences
  included and failures never. It reads the uncached units of one commit together. A commit whose whole tree
  `ConsoleBundle` holds is answered from it, and nothing is ever inserted into the bundle.
  `ChangeReads.Renders` and `Cached` take each commit's units (`mergeUnits`), so the existing one-uncached-commit
  per page budget (`toFetch`) counts a commit kept for another unit as uncached.
- **Command** (`v2/cmd/swhurl-v2/console.go`, `console_activity.go`): `pin(source)` sets the bundle and the
  evidence cache together. `localConsole.Files` reads the checkout's archive, so `--dev` asks GitHub for no tree or
  file. Activity and job pages use the evidence cache. `mainRenders` and every other page keep the whole-tree
  bundle.
- **Deliberate change in meaning:** a malformed record refuses only its own unit. The archive path refused the
  whole commit, so every change merged at it read **Unknown**. Pinned by
  `TestConsoleEvidenceRefusesOnlyTheUnitAskedFor`; the operator may still ask for the old scope.

### Verification

All commands ran under the machine lease, offline.

- `go test -race -count=1 ./adapters/github/...` and `./orchestration`: passed. This covers the adapter's real
  transport (requested blobs only, links and submodules, ten broken listings, 404/403/429/500 and corrupt or
  oversized blobs with redacted errors, cancellation, 12 paths over 6 ids read with a peak of 4, and refused
  commits, paths and missing tokens). It covers archive parity for every unit (alone and together) and for each of
  the four files missing, and the cache's keys, absences, retries, bound, whole-tree reuse and 32 concurrent
  readers. It covers several units at one commit as one read, a new unit at a kept commit costing the page's read,
  and a failed read keeping an already-reached change `unchecked` with nothing recorded or kept.
- `cmd/swhurl-v2` (with `-race` for these tests): the Activity and job-merged goldens through the server's own
  `changeReads()` with only cluster rows injected. The first page makes one targeted read and one `main` tarball,
  and the second makes neither again. A failed merge-commit read is retried. A `--dev` page renders the same
  golden from the checkout with no GitHub tree or file read.
- Two mutations were caught: a cache treating a partly kept commit as in hand failed two tests, and a source
  answering no files failed the three page tests.
- `make check-v2`: passed.
- `make check` (check-repo, test, check-apps, check-otel, check-lint, check-codegraph, check-docs; all offline): passed, `check-docs` with 0 errors and 0 warnings.
- **Pre-existing race, not fixed:** `go test -race -count=1 ./cmd/swhurl-v2 -run TestAUnitAction` fails on an
  untouched `git archive 77fa0904 v2` copy (3 of 3 runs, 3 to 5 `DATA RACE` reports in
  `TestAUnitActionsRefusalIsTheJobsLine`), as it does in this worktree, where the report sometimes falls in
  `TestAUnitActionStartsAJob` instead. Every reported frame is in `orchestration/jobs.go`, `adapters/kube/jobs.go`,
  `adapters/kube/kubetest/data.go` or the unit-action handler and its test, none in this change. `make check-v2`
  runs without `-race`, and the package passes there.

### Remaining gate

Rollout needs the operator's separate confirmation: commit, push, Validate's console deploy, `make
flux-reconcile`, then a cold Activity and job page on the deployed console with timings recorded in
[current state](current-state.md). Not exercised: the real GitHub API (only scripted transports), a pod restart,
rate limits and concurrent users. One merge commit per page is kept as approved, and raising it waits for that
measurement.
