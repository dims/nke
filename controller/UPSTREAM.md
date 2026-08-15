# Controller upstream sync

During the open-source migration, **GitLab remains the writable source of truth** for controller code until a GitHub CI/release pipeline replaces it. This tree is a flat snapshot (no rewritten GitLab history). Pull updates here with a deliberate sync, not `git merge`. Syncing is going to be painful so our plan is to try and do it only one more time just before cutover (assuming the pipeline can be rewritten on GitHub in a reasonable amount of time).

## Pin

| Field | Value |
| --- | --- |
| Upstream | https://gitlab-master.nvidia.com/nke/cloud-workflow-engine |
| Branch | `main` |
| Last imported revision | `fa91e66f` (`v0.0.653`) |
| Previously imported | `d1f63d61` (`v0.0.616`), 2026-08-05 |
| Destination | `controller/` in https://github.com/NVIDIA/nke |

⚠️ **The second sync was not a snapshot.** Between the two pins upstream moved 94
commits, and this tree moved 21. The two had diverged in **opposite** directions:
upstream still carries the **private carbide SDK in 74 files**, while this tree
had migrated off it. Copying upstream over the top would have reverted the whole
open-source migration, so every one of the 21 changes was re-applied by hand on
top of the new snapshot.

⚠️ **What that cost, so the next sync budgets for it.** The dangerous losses were
silent — they compiled and passed tests. A `docker build` context reverted from
the repository root to `controller/`; a producer/consumer split where a script
writes `agent.tgz` and a Dockerfile copied `node-agent.tgz`; `NODE_AGENT_REPO_PATH`
pointing at a directory that no longer exists; a base image losing its digest pin;
and three dependencies silently downgraded, undoing merged Dependabot work.
**Run the full lint and test suite after a sync, not just the build** — the build
saw none of these.

## Transforms applied after upstream import

1. Drop GitLab CI leftovers: `.gitlab-ci.yml`, `.gitlab/`. Also `.cursor/`, `.vscode/`.
2. Rewrite the self-module path everywhere it appears (Go imports, `go.mod`, scripts, docs as needed):

   `gitlab-master.nvidia.com/nke/cloud-workflow-engine` → `github.com/NVIDIA/nke/controller`
3. Update goimports local prefixes so lint stays green after the module rewrite (`.golangci.yml` — **two** settings blocks — and `Makefile` `fmt`):

   `gitlab-master.nvidia.com` → `github.com/NVIDIA/nke/controller`

   ⚠️ This step used to say "keep `gitlab-master.nvidia.com`, and add the new
   path". It is a **replacement**, not an addition — no local import in this
   tree is a `gitlab-master.nvidia.com` path any more.

**Three more rewrites are needed and were not written down here before.** They are
not self-module paths, so step 2 does not cover them:

4. `gitlab-master.nvidia.com/nke/node-agent` → `github.com/NVIDIA/nke/agent`, and
   the literal string `node-agent` → `agent` in prose, filenames and log text.
   ⚠️ That literal replace turns "a node-agent" into "a agent" — fix the article.
5. `gitlab-master.nvidia.com/nvmetal/cloud-api-sdk-go` →
   `github.com/NVIDIA/ncx-infra-controller-rest/sdk/standard`. The old SDK's
   `/helper` subpackage has no counterpart; `internal/providers/forge/sdk/ngcauth.go`
   is a local port of what it provided.
6. `gitlab-master.nvidia.com/nke/cluster-api-provider-nico` →
   `github.com/NVIDIA/cluster-api-provider-nico`.

⚠️ **`api/server.gen.go` embeds the OpenAPI spec gzip+base64.** Editing
`api/openapi.yaml` is not enough — the embedded copy keeps the old text and no
grep will find it. Regenerate with `GOWORK=off go generate ./...` from `api/`
(the `-modfile` it uses is rejected in workspace mode) and commit the result.

## Not imported

Deliberate omissions. They are not losses, and the next sync should keep them
out unless the reason has changed.

| Path | Why |
| --- | --- |
| `nkectl/` | Its own Go module. The plan puts the CLI at `cli/` in the repository root, not inside `controller/`, so importing it here would bake in a path that has to be undone. Nothing in this tree references it. |
| `THIRD_PARTY_NOTICES.md` | The original import excluded it, and the root `THIRD_PARTY_NOTICES.md` states `controller/` is not yet covered. ⚠️ **That reason is now stale** — the generator does work here with `GOWORK=off`. Adopting it is a follow-up, not a sync step. |
| `CODEOWNERS` | Deleted deliberately: its only content was `* @nke/nke-developers`, and there is no GitHub org named `nke`, so that owner can never resolve. `.github/CODEOWNERS` already routes `/controller/`. |
| `.gitlab-ci.yml`, `.gitlab/` | Transform 1. ⚠️ Prose referring to them survives in `AGENTS.md` and `Chart.yaml`; both now say so explicitly. |
| `.cursor/`, `.vscode/`, `.flox-cli-version` | Editor and tool-version local state. ⚠️ **`.flox-cli-version` was re-added** — `README.md` instructs `cat .flox-cli-version`, so excluding it broke a documented step. |
