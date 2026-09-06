#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Builds the two nke-cni-calico-<stream> debs from a bundle tree written by
# bundle.sh, using nfpm and the specs beside this script.
#
# Usage:
#   build-deb.sh <calico-version> <bundle-dir> <out-dir>
#
# bundle-dir and out-dir may be relative to the caller's cwd; they are
# canonicalised here because nfpm runs with PACKAGING_DIR as its cwd.
#
# Environment:
#   DEB_REVISION  Debian packaging revision (default 1).
#   NFPM_BIN      Path to the nfpm binary (default: nfpm on PATH).

set -euo pipefail

ARCH=amd64
NFPM_BIN="${NFPM_BIN:-nfpm}"

usage() {
  echo "usage: $(basename "$0") <calico-version> <bundle-dir> <out-dir>" >&2
  exit 2
}

[[ $# -eq 3 ]] || usage
CALICO_VER="$1"
SRC="$(cd "$2" 2>/dev/null && pwd)" ||
  { echo "build-deb.sh: no such bundle directory: $2" >&2; exit 1; }
mkdir -p "$3"
OUT_DIR="$(cd "$3" && pwd)"
DEB_REVISION="${DEB_REVISION:-1}"

command -v "${NFPM_BIN}" >/dev/null 2>&1 || {
  echo "build-deb.sh: nfpm not found (${NFPM_BIN}); run 'make nfpm' or set NFPM_BIN" >&2
  exit 1
}

PACKAGING_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

[[ "${CALICO_VER}" == v* ]] || CALICO_VER="v${CALICO_VER}"
# Package names carry the stream without the leading v, matching what
# agent/internal/agent/cni.go asks apt for.
CALICO_STREAM="${CALICO_VER#v}"

INSTALL_ROOT="/usr/share/nke-cni/calico/${CALICO_STREAM}"
IMAGE_DIR="/usr/lib/nke/cni-calico/${CALICO_STREAM}"

for f in "${SRC}/manifests/tigera-operator.yaml" "${SRC}/manifests/custom-resources.yaml"; do
  [[ -f "$f" ]] || {
    echo "build-deb.sh: ${f} is missing; run 'make bundle' first." >&2
    exit 1
  }
done
if [[ ! -f "${SRC}/images.tgz" || ! -f "${SRC}/images.tgz.sha256" ]]; then
  echo "build-deb.sh: ${SRC}/images.tgz and its .sha256 sidecar are missing." >&2
  echo "  The images .deb would install nothing and Calico could not come up offline." >&2
  echo "  Run 'make bundle' first." >&2
  exit 1
fi

SCRATCH="$(mktemp -d)"
trap 'rm -rf "${SCRATCH}"' EXIT

# nfpm does no expansion in `scripts:`, so the maintainer script is rendered
# here and referenced by a fixed name in the spec's directory.
render_postinst() {
  local package_name="$1" out="${PACKAGING_DIR}/postinst.rendered"
  sed \
    -e "s|@PACKAGE_NAME@|${package_name}|g" \
    -e "s|@IMAGE_DIR@|${IMAGE_DIR}|g" \
    -e "s|@CALICO_STREAM@|${CALICO_STREAM}|g" \
    -e "s|@ARCH@|${ARCH}|g" \
    "${PACKAGING_DIR}/postinst" >"${out}"
  chmod 0755 "${out}"
}

build_one() {
  local spec="$1" package_name="$2"
  echo "cni-calico: building ${package_name}_${DEB_REVISION}_${ARCH}.deb"
  PACKAGE_NAME="${package_name}" \
  CALICO_STREAM="${CALICO_STREAM}" \
  DEB_REVISION="${DEB_REVISION}" \
  ARCH="${ARCH}" \
  INSTALL_ROOT="${INSTALL_ROOT}" \
  IMAGE_DIR="${IMAGE_DIR}" \
  SRC="${SRC}" \
  SCRATCH="${SCRATCH}" \
    sh -c 'cd "$1" && shift && exec "$@"' _ "${PACKAGING_DIR}" \
      "${NFPM_BIN}" package -f "${spec}" -p deb -t "${OUT_DIR}"
  echo "wrote ${OUT_DIR}/${package_name}_${DEB_REVISION}_${ARCH}.deb"
}

build_one nfpm-manifests.yaml "nke-cni-calico-manifests-${CALICO_STREAM}"

render_postinst "nke-cni-calico-images-${CALICO_STREAM}"
trap 'rm -rf "${SCRATCH}"; rm -f "${PACKAGING_DIR}/postinst.rendered"' EXIT
build_one nfpm-images.yaml "nke-cni-calico-images-${CALICO_STREAM}"
