# Download and Install nkectl

`nkectl` is the NKE control-plane CLI. You use it to validate and bootstrap a site controller cluster from a declarative inventory file, and to manage tenant cluster lifecycle.

Releases are published under `cli/v*` tags at https://github.com/NVIDIA/nke/releases.

## Download

The repository is internal, so download the asset with `gh`, which sends your
credentials. A plain `curl` of the asset URL returns 404 until the repository
is public.

```bash
NKECTL_RELEASE=cli/v0.1.0-rc.2
NKECTL_PLATFORM=linux-amd64   # or linux-arm64, darwin-amd64, darwin-arm64

gh release download "$NKECTL_RELEASE" --repo NVIDIA/nke \
  --pattern "nkectl-$NKECTL_PLATFORM" --pattern checksums.txt --clobber
```

Verify the download against `checksums.txt` from the same release:

```bash
sha256sum --ignore-missing -c checksums.txt
# macOS: shasum -a 256 --ignore-missing -c checksums.txt
```

Install it:

```bash
chmod +x "nkectl-$NKECTL_PLATFORM"
sudo mv "nkectl-$NKECTL_PLATFORM" /usr/local/bin/nkectl
```

## Verify

```bash
nkectl version
```

## Build from Source

```bash
git clone https://github.com/NVIDIA/nke.git
cd nke
make build          # links all binaries including nkectl into bin/
```

The binary lands at `bin/nkectl`.

## Additional Prerequisites for Bootstrap

`nkectl bootstrap site` creates an ephemeral Kind cluster on the init node to run the bootstrap workflow engine. The init node needs:

- **`kind`** — `brew install kind` (macOS) or download from https://kind.sigs.k8s.io/docs/user/quick-start/#installation
- **`nerdctl`** (default) or **`docker`** — the container runtime `nkectl` uses to create and load images into the Kind cluster. Use `--bootstrap-provider docker` if you are running Docker rather than nerdctl/containerd.

Join nodes do not need `kind` or a container runtime — only the init node runs `nkectl`.

## Next Steps

- [Configuration](configuration.md) — write your site inventory file
- [Pre-Provisioned Nodes](pre-provisioned-nodes.md) — prepare nodes and run the bootstrap
