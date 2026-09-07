#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Fetches, verifies and stages the ovn-kubernetes chart and images the
# nke-cni-ovn-<stream> packages ship.
#
# Usage: bundle.sh --version 1.4.0 --dist DIR --lock-dir DIR --crane PATH
#                  [--arch amd64] [--write-lock]
#
# NGC_API_KEY must be set: the chart and the images live in NGC's nv-ngn org.
# Every artifact is checked against the committed lock before anything is
# unpacked. --write-lock regenerates the lock instead of enforcing it, which
# needs network and is a reviewed change, never part of a build.

set -euo pipefail

die() {
  echo "cni-ovn: $*" >&2
  exit 1
}

VERSION=""
DIST=""
LOCK_DIR=""
CRANE=""
ARCH="amd64"
WRITE_LOCK=false

while [[ $# -gt 0 ]]; do
  case "$1" in
    --version) VERSION="${2:-}"; shift 2 ;;
    --dist) DIST="${2:-}"; shift 2 ;;
    --lock-dir) LOCK_DIR="${2:-}"; shift 2 ;;
    --crane) CRANE="${2:-}"; shift 2 ;;
    --arch) ARCH="${2:-}"; shift 2 ;;
    --write-lock) WRITE_LOCK=true; shift ;;
    *) die "unknown argument: $1" ;;
  esac
done

[[ -n "$VERSION" ]] || die "--version is required"
[[ -n "$DIST" ]] || die "--dist is required"
[[ -n "$LOCK_DIR" ]] || die "--lock-dir is required"
[[ -n "$CRANE" && -x "$CRANE" ]] || die "--crane must point at an executable crane"
[[ "$VERSION" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] ||
  die "--version must be a bare chart version such as 1.4.0, got ${VERSION}"
[[ -n "${NGC_API_KEY:-}" ]] || die "NGC_API_KEY is required (an NGC key with access to the nv-ngn org)"

LOCK="${LOCK_DIR}/${VERSION}.lock"
STAGE="${DIST}/${VERSION}"
WORK="${STAGE}/.work"
# Overridable so a test can point both at a local server and a fake crane.
HELM_BASE="${NGC_HELM_URL:-https://helm.ngc.nvidia.com/nv-ngn/sdn/charts}"
REGISTRY="${NGC_REGISTRY:-nvcr.io}"
CHART="ovn-kubernetes"
CHART_TGZ="${CHART}-${VERSION}.tgz"

# The key never goes on a command line: curl reads it from a config on stdin,
# crane from a throwaway DOCKER_CONFIG that is removed on exit.
AUTH_DIR="$(mktemp -d)"
trap 'rm -rf "$AUTH_DIR"' EXIT
export DOCKER_CONFIG="$AUTH_DIR"

rm -rf "$STAGE"
mkdir -p "$WORK" "${STAGE}/charts"

fetch() {
  local url="$1" out="$2"
  curl --fail --silent --show-error --location \
    --connect-timeout 10 --max-time 300 \
    --retry 3 --retry-delay 5 --retry-connrefused \
    --config - -o "$out" "$url" <<<"user = \"\$oauthtoken:${NGC_API_KEY}\"" ||
    die "fetch ${url} failed"
}

sha_of() {
  sha256sum "$1" | cut -d' ' -f1
}

# lock_value <kind> <key> prints the pinned value, or nothing when absent.
lock_value() {
  local kind="$1" key="$2"
  [[ -f "$LOCK" ]] || return 0
  awk -v kind="$kind" -v key="$key" '$1 == kind && $2 == key { print $3; exit }' "$LOCK"
}

# -------------------------------------------------------------------- chart

fetch "${HELM_BASE}/${CHART_TGZ}" "${WORK}/${CHART_TGZ}"
CHART_SHA="$(sha_of "${WORK}/${CHART_TGZ}")"
if [[ "$WRITE_LOCK" == false ]]; then
  want="$(lock_value artifact "$CHART_TGZ")"
  [[ -n "$want" ]] || die "${LOCK} has no artifact line for ${CHART_TGZ}; run 'make lock' first"
  [[ "$want" == "$CHART_SHA" ]] ||
    die "${CHART_TGZ} sha256 mismatch: lock has ${want}, NGC served ${CHART_SHA}"
fi

unsafe="$(tar -tzf "${WORK}/${CHART_TGZ}" | grep -E '^/|(^|/)\.\.(/|$)' || true)"
[[ -z "$unsafe" ]] || die "${CHART_TGZ} has an unsafe member path: ${unsafe}"
tar -xzf "${WORK}/${CHART_TGZ}" --no-same-owner -C "${STAGE}/charts"
[[ -f "${STAGE}/charts/${CHART}/Chart.yaml" ]] || die "${CHART_TGZ} did not unpack to ${CHART}/Chart.yaml"
grep -qx "version: ${VERSION}" "${STAGE}/charts/${CHART}/Chart.yaml" ||
  die "${CHART_TGZ} is not chart version ${VERSION}"
# ovnHelmChartStaged in agent/internal/agent/cni.go keys on this file.
[[ -f "${STAGE}/charts/${CHART}/values-nke.yaml" ]] || die "${CHART_TGZ} has no values-nke.yaml"
# Fixed modes and mtimes, so the charts deb does not depend on how the chart
# was packed: nfpm's `type: tree` copies both from the source files.
find "${STAGE}/charts" -type d -exec chmod 0755 {} +
find "${STAGE}/charts" -type f -exec chmod 0644 {} +
find "${STAGE}/charts" -exec touch -d '2026-01-01 00:00:00Z' {} +

# --------------------------------------------------------------- image list
#
# The list is the lock's, which `make lock` seeds and a human reviews. With
# values-nke.yaml the chart runs only ovnkube-control-plane and
# ovnkube-single-node-zone, so ovn-c and ovnkube-c are all it pulls.

if [[ "$WRITE_LOCK" == true ]]; then
  mapfile -t IMAGE_REFS < <(awk '$1 == "image" { print $2 }' "$LOCK" 2>/dev/null || true)
  [[ ${#IMAGE_REFS[@]} -gt 0 ]] ||
    die "--write-lock needs the image list seeded in ${LOCK}; add 'image <ref>' lines first"
else
  mapfile -t IMAGE_REFS < <(awk '$1 == "image" { print $2 }' "$LOCK")
  [[ ${#IMAGE_REFS[@]} -gt 0 ]] || die "${LOCK} lists no images"
fi

"$CRANE" auth login "$REGISTRY" --username '$oauthtoken' --password-stdin <<<"$NGC_API_KEY" >/dev/null ||
  die "crane auth login ${REGISTRY} failed"

declare -A IMAGE_DIGEST=()
PINNED_REFS=()
for ref in "${IMAGE_REFS[@]}"; do
  digest="$("$CRANE" digest --platform "linux/${ARCH}" "$ref")" ||
    die "crane digest ${ref} failed"
  [[ "$digest" =~ ^sha256:[0-9a-f]{64}$ ]] ||
    die "crane digest ${ref} returned ${digest}"
  IMAGE_DIGEST["$ref"]="$digest"

  if [[ "$WRITE_LOCK" == false ]]; then
    want="$(lock_value image "$ref")"
    [[ -n "$want" ]] || die "${LOCK} has no digest for ${ref}; run 'make lock' first"
    [[ "$want" == "$digest" ]] ||
      die "${ref} digest mismatch: lock has ${want}, registry serves ${digest}"
  fi
  # Pull by digest so the archive cannot drift from what was verified.
  PINNED_REFS+=("${ref%:*}@${digest}")
done

# ------------------------------------------------------------ images archive
#
# One gzipped docker-archive, repacked deterministically so two builds of the
# same lock are byte-identical: fixed member order, mtime, owner and mode, and
# gzip -n so the name and timestamp stay out of the header.

"$CRANE" pull --platform "linux/${ARCH}" "${PINNED_REFS[@]}" "${WORK}/images.tar" ||
  die "crane pull failed"

unsafe="$(tar -tf "${WORK}/images.tar" | grep -E '^/|(^|/)\.\.(/|$)' || true)"
[[ -z "$unsafe" ]] || die "crane wrote an unsafe member path: ${unsafe}"

mkdir -p "${WORK}/unpacked"
tar -xf "${WORK}/images.tar" --no-same-owner -C "${WORK}/unpacked"

# crane names a digest-pulled image "<repo>:i-was-a-digest" in the archive
# manifest. Rewrite those to the tag the chart renders, or the import lands
# under a name the kubelet never asks for.
manifest="${WORK}/unpacked/manifest.json"
[[ -f "$manifest" ]] || die "crane archive has no manifest.json"
for ref in "${IMAGE_REFS[@]}"; do
  repo="${ref%:*}"
  placeholder="${repo}:i-was-a-digest"
  grep -q -- "$placeholder" "$manifest" ||
    die "crane did not name ${ref} as expected; it would import under the wrong name"
  # Substitute the literal string; both sides are plain image references.
  python3 - "$manifest" "$placeholder" "$ref" <<'PY'
import sys
path, old, new = sys.argv[1], sys.argv[2], sys.argv[3]
with open(path) as fh:
    body = fh.read()
with open(path, "w") as fh:
    fh.write(body.replace(old, new))
PY
done
leftover="$(grep -l 'i-was-a-digest' "${WORK}/unpacked"/* 2>/dev/null || true)"
[[ -z "$leftover" ]] || die "crane's digest placeholder survived in ${leftover}"

(cd "${WORK}/unpacked" && find . -type f | LC_ALL=C sort >"${WORK}/members")
tar --format=gnu --numeric-owner --owner=0 --group=0 \
  --mtime='2026-01-01 00:00:00Z' --mode='u+rw,go+r-w,a-s' \
  -C "${WORK}/unpacked" -cf "${WORK}/repacked.tar" -T "${WORK}/members"
gzip -n -9 -c "${WORK}/repacked.tar" >"${STAGE}/images.tgz"

# The sidecar is consumed by postinst as `sha256sum -c`, from the directory the
# deb installs into, so it has to be in sha256sum's two-field format and name
# the file by its *installed* name rather than the staged one.
printf '%s  ovn-images_%s_linux_%s.tgz\n' \
  "$(sha_of "${STAGE}/images.tgz")" "${VERSION}" "${ARCH}" \
  >"${STAGE}/images.tgz.sha256"

rm -rf "$WORK"

# ------------------------------------------------------------------- lock io

if [[ "$WRITE_LOCK" == true ]]; then
  mkdir -p "$LOCK_DIR"
  {
    echo "# nke-cni-ovn lock for ${VERSION} (${ARCH}). Regenerate with \`make lock\`; review the diff before committing."
    echo "ovn ${VERSION}"
    echo "artifact ${CHART_TGZ} ${CHART_SHA} ${HELM_BASE}/${CHART_TGZ}"
    for ref in "${IMAGE_REFS[@]}"; do
      echo "image ${ref} ${IMAGE_DIGEST[$ref]}"
    done
  } >"${LOCK}.new"
  mv "${LOCK}.new" "$LOCK"
  echo "cni-ovn: wrote ${LOCK}"
else
  echo "cni-ovn: staged ${VERSION} into ${STAGE} (verified against ${LOCK})"
fi
