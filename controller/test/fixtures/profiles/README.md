# Profiles fixture

The profiles tree the CWE E2E lanes deploy (`CWE_PROFILES_DIR`), built into the
`cwe-profiles` image by the `Dockerfile` one directory up. The fleet profiles
live in `nke-site-deploy/cwe-profiles`; this tree is a fixture for tests.

Two kinds of entry live here, and the difference matters:

| Profile | URLs | Boots |
|---|---|---|
| `test-fixture-amd64`, `nke-local-dev-kindest-arm64` | `example.test`, synthetic | nothing; they exercise CWE's rendering and the CAPD and gofail lanes |
| `nke-nico-vms-24.04-amd64-fde` | real, public CloudFront hosts | the NKE FDE node image on the `nico-vms` backend |

`nke-nico-vms-24.04-amd64-fde` is a copy of the fleet profile of the same name.
It is the OS profile every `nico-vms` E2E spec requests, and CWE reads its
`ipxeScript` and `userData.tmpl` unconditionally when it creates a node, so
without the copy the first create fails. The files hold no credentials: the
image is fetched with a plain GET, and the one `token_name` line is a template
variable. When the fleet profile changes, copy it again; nothing syncs it.

The copy carries one line the fleet profile does not: before the `apt-get
install` lines, `userData.tmpl` checks for a flat apt repository at
`http://carbide-pxe.forge:18080/files/apt/` (nico-vms' file server) and, when
its `Packages` index answers, adds it as a source pinned above the image's S3
repository. `hack/ci/nico-vms-e2e.sh` fills that directory once per run with
the packages the specs will install, about 2.8 GB, so 16 boots do not each pull
2 GB from S3. Without the index the line logs one message and apt behaves as
in the fleet.

`configProfiles/nico-vms-cp.yaml` and `nico-vms-worker.yaml` are copies of the
fleet config profiles the same specs name, and `../runbooks/` holds the eight
runbooks their hooks reference. CWE validates at startup that every hook
resolves to a script under `runbooks/`, so the two sets travel together. The
fleet files carry NVIDIA's proprietary header; the copies carry this
repository's Apache-2.0 header, as NVIDIA-owned fixture data.
