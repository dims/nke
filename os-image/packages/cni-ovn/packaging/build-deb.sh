#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Builds the two nke-cni-ovn-<stream> debs from a bundle tree written by
# bundle.sh, using nfpm and the specs beside this script.
#
# Usage:
#   build-deb.sh <ovn-stream> <bundle-dir> <out-dir>
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
  echo "usage: $(basename "$0") <ovn-stream> <bundle-dir> <out-dir>" >&2
  exit 2
}

[[ $# -eq 3 ]] || usage
OVN_STREAM="$1"
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

# ovnKubernetesHelmChartDir in agent/internal/agent/cni.go: the agent runs helm
# there when values-nke.yaml is present, whatever the stream.
CHART_DIR="/etc/dgx/ovn-kubernetes"
IMAGE_DIR="/usr/lib/nke/cni-ovn/${OVN_STREAM}"

[[ -f "${SRC}/charts/ovn-kubernetes/Chart.yaml" ]] || {
  echo "build-deb.sh: ${SRC}/charts/ovn-kubernetes/Chart.yaml is missing; run 'make bundle' first." >&2
  exit 1
}
if [[ ! -f "${SRC}/images.tgz" || ! -f "${SRC}/images.tgz.sha256" ]]; then
  echo "build-deb.sh: ${SRC}/images.tgz and its .sha256 sidecar are missing." >&2
  echo "  The images .deb would install nothing and OVN could not come up offline." >&2
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
    -e "s|@OVN_STREAM@|${OVN_STREAM}|g" \
    -e "s|@ARCH@|${ARCH}|g" \
    "${PACKAGING_DIR}/postinst" >"${out}"
  chmod 0755 "${out}"
}

build_one() {
  local spec="$1" package_name="$2"
  echo "cni-ovn: building ${package_name}_${DEB_REVISION}_${ARCH}.deb"
  PACKAGE_NAME="${package_name}" \
  OVN_STREAM="${OVN_STREAM}" \
  DEB_REVISION="${DEB_REVISION}" \
  ARCH="${ARCH}" \
  CHART_DIR="${CHART_DIR}" \
  IMAGE_DIR="${IMAGE_DIR}" \
  SRC="${SRC}" \
  SCRATCH="${SCRATCH}" \
    sh -c 'cd "$1" && shift && exec "$@"' _ "${PACKAGING_DIR}" \
      "${NFPM_BIN}" package -f "${spec}" -p deb -t "${OUT_DIR}"
  echo "wrote ${OUT_DIR}/${package_name}_${DEB_REVISION}_${ARCH}.deb"
}

build_one nfpm-charts.yaml "nke-cni-ovn-charts-${OVN_STREAM}"

render_postinst "nke-cni-ovn-images-${OVN_STREAM}"
trap 'rm -rf "${SCRATCH}"; rm -f "${PACKAGING_DIR}/postinst.rendered"' EXIT
build_one nfpm-images.yaml "nke-cni-ovn-images-${OVN_STREAM}"
