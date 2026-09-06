# nke-cni-calico

Builds the two Debian packages `nke-agent` installs when it sets up Calico:

| Package | Contents | Installed to |
|---|---|---|
| `nke-cni-calico-manifests-<stream>` | Upstream `tigera-operator.yaml` and `custom-resources.yaml` | `/usr/share/nke-cni/calico/<stream>/manifests/` |
| `nke-cni-calico-images-<stream>` | Every image the operator resolves, as one docker-archive | `/usr/lib/nke/cni-calico/<stream>/`, imported into containerd by `postinst` |

`provision.sh` stages both into the image under `/usr/lib/nke/debs` as a local
apt repo, the same way it stages `nke-kubeadm-distro`. Nothing is installed at
bake time: `nke-agent` installs them by name at cluster create.

## Why both

The manifests name only `quay.io/tigera/operator`. The operator then resolves
the rest of Calico itself, so a node with no registry access needs the whole set
present in containerd before the operator starts. The images package is what
makes that true; without it a cluster comes up and Calico never does.

## The contract with nke-agent

`agent/internal/agent/cni.go` asks apt for `nke-cni-calico-manifests-<stream>`
and `nke-cni-calico-images-<stream>`, where `<stream>` is the request's Calico
version or `defaultCalicoCniStream`. It then finds the install root by looking
through `dpkg-query -L` for a path ending in `manifests/tigera-operator.yaml`,
and applies that file and `custom-resources.yaml` beside it.

So both manifests must be owned by the package and live in the same
`manifests/` directory. `TestCalicoInstallRootFromFiles` in the agent has a case
pinned to this package's exact layout.

## Building

```bash
make debs OUT_DIR=/absolute/path
```

Linux only. `debs` runs `bundle` first, which verifies every artifact against
`lock/<version>.lock` before anything is unpacked, and reindexes `OUT_DIR` with
`dpkg-scanpackages` when it finishes. That index covers every deb in the
directory, not just this package's: `kubeadm-distro` writes the same directory
and indexes it too, so whichever runs last has to see them all.

`make repro-check` builds twice and compares every `.deb` byte-for-byte.

## Bumping Calico

Set `CALICO_VERSIONS`, refresh the lock, review it, commit it:

```bash
make lock CALICO_VERSIONS=v3.30.2
git diff -- lock
```

`make lock` needs network. It re-resolves every image digest and re-checksums
the manifests; the image *list* comes from the lock's existing `image` lines, so
a release that adds a component needs that line added by hand first. Deriving
the list from the manifests would miss exactly those images, because the
operator resolves them at runtime rather than naming them.

Keep the default stream in step with `defaultCalicoCniStream` in
`agent/internal/agent/cni.go`; the agent asks for that one when a request names
no version.

## Size

The images package is around 490 MiB — roughly half a gigabyte added to the
node image. That is the cost of installing Calico with no registry. If a site
has a reachable registry, the manifests package alone is enough.
