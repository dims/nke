#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Fetches, verifies and stages the upstream Calico artifacts the
# nke-cni-calico-<stream> packages ship.
#
# Usage: bundle.sh --version v3.30.1 --dist DIR --lock-dir DIR --crane PATH
#                  [--arch amd64] [--write-lock]
#
# Every artifact is checked against the committed lock before anything is
# unpacked. --write-lock regenerates the lock instead of enforcing it, which
# needs network and is a reviewed change, never part of a build.

set -euo pipefail

die() {
  echo "cni-calico: $*" >&2
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
[[ "$VERSION" == v* ]] || die "--version must be leading-v, got ${VERSION}"

LOCK="${LOCK_DIR}/${VERSION}.lock"
STAGE="${DIST}/${VERSION}"
WORK="${STAGE}/.work"
MANIFEST_BASE="https://raw.githubusercontent.com/projectcalico/calico/${VERSION}/manifests"

# The manifests the agent applies. calicoInstallRootFromFiles in
# agent/internal/agent/cni.go finds the install root by looking for
# manifests/tigera-operator.yaml, and runCalicoKubectlApplyFromDir applies both.
MANIFESTS=(tigera-operator.yaml custom-resources.yaml)

rm -rf "$STAGE"
mkdir -p "$WORK" "${STAGE}/manifests"

fetch() {
  local url="$1" out="$2"
  curl --fail --silent --show-error --location \
    --connect-timeout 10 --max-time 120 \
    --retry 3 --retry-delay 5 --retry-connrefused \
    -o "$out" "$url" || die "fetch ${url} failed"
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

# ---------------------------------------------------------------- manifests

declare -A MANIFEST_SHA=()
for name in "${MANIFESTS[@]}"; do
  out="${STAGE}/manifests/${name}"
  fetch "${MANIFEST_BASE}/${name}" "$out"
  got="$(sha_of "$out")"
  MANIFEST_SHA["$name"]="$got"

  if [[ "$WRITE_LOCK" == false ]]; then
    want="$(lock_value artifact "$name")"
    [[ -n "$want" ]] || die "${LOCK} has no artifact line for ${name}"
    [[ "$want" == "$got" ]] ||
      die "${name} sha256 mismatch: lock has ${want}, upstream served ${got}"
  fi
done

# --------------------------------------------------------------- image list
#
# The operator manifest names only the operator; the operator pulls the rest at
# runtime, so an offline node needs the whole set pre-imported. The list is the
# lock's, which `make lock` seeds and a human reviews -- deriving it from the
# manifests would silently miss the images the operator resolves itself.

if [[ "$WRITE_LOCK" == true ]]; then
  mapfile -t IMAGE_REFS < <(awk '$1 == "image" { print $2 }' "$LOCK" 2>/dev/null || true)
  [[ ${#IMAGE_REFS[@]} -gt 0 ]] ||
    die "--write-lock needs the image list seeded in ${LOCK}; add 'image <ref>' lines first"
else
  mapfile -t IMAGE_REFS < <(awk '$1 == "image" { print $2 }' "$LOCK")
  [[ ${#IMAGE_REFS[@]} -gt 0 ]] || die "${LOCK} lists no images"
fi

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
    [[ -n "$want" ]] || die "${LOCK} has no image line for ${ref}"
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
# manifest. Rewrite those to the tag the operator actually asks containerd for,
# or the import lands under a name nothing resolves.
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
printf '%s  calico-images_%s_linux_%s.tgz\n' \
  "$(sha_of "${STAGE}/images.tgz")" "${VERSION#v}" "${ARCH}" \
  >"${STAGE}/images.tgz.sha256"

rm -rf "$WORK"

# ------------------------------------------------------------------- lock io

if [[ "$WRITE_LOCK" == true ]]; then
  mkdir -p "$LOCK_DIR"
  {
    echo "# nke-cni-calico lock for ${VERSION} (${ARCH}). Regenerate with \`make lock\`; review the diff before committing."
    echo "calico ${VERSION}"
    for name in "${MANIFESTS[@]}"; do
      echo "artifact ${name} ${MANIFEST_SHA[$name]} ${MANIFEST_BASE}/${name}"
    done
    for ref in "${IMAGE_REFS[@]}"; do
      echo "image ${ref} ${IMAGE_DIGEST[$ref]}"
    done
  } >"${LOCK}.new"
  mv "${LOCK}.new" "$LOCK"
  echo "cni-calico: wrote ${LOCK}"
else
  echo "cni-calico: staged ${VERSION} into ${STAGE} (verified against ${LOCK})"
fi
