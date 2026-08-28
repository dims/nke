#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# Shared body for the two backend E2E entrypoints, ci/backend-e2e.sh and
# local-backend-e2e.sh. Source it; do not run it.
#
# The two entrypoints differ in how they get a Kind cluster with CWE on it: CI
# builds every image and runs the tools from an inner container, while the local
# one uses the Makefile targets and the host's tools. What they must NOT differ
# in is everything after that, and they had: the local one never set
# `site_controller_vip`, so CAPD tenants were handed the Kind bridge gateway,
# where nothing is bound on native Docker. Nothing compared the two.
#
# So the parts below live here once. Each takes an exec function, because that
# is the one thing that legitimately differs: CI runs kubectl inside its runner,
# local runs it on the host.

if [[ "${BASH_SOURCE[0]}" == "${0}" ]]; then
  echo "error: source backend-e2e-lib.sh from another script" >&2
  exit 1
fi

# The Kind control-plane container's address on the `kind` network. CAPD tenants
# are sibling containers there, so this is what reaches the management cluster's
# NodePorts from a tenant node. The Tiltfile falls back to the bridge gateway
# when this is unset, which works on Colima and not on native Docker.
e2e_management_vip() {
  local cluster="$1" vip
  vip="$(docker inspect \
    --format '{{(index .NetworkSettings.Networks "kind").IPAddress}}' \
    "${cluster}-control-plane" 2>/dev/null || true)"
  [[ -n "${vip}" ]] || return 1
  printf '%s' "${vip}"
}

# tilt_config.json, written the same way for both entrypoints.
e2e_write_tilt_config() {
  local out="$1" providers="$2" cwe_image="$3" vip="$4" bgp_container="$5"
  jq -n \
    --arg cwe_image "${cwe_image}" \
    --arg site_controller_vip "${vip}" \
    --arg bgp_container_name "${bgp_container}" \
    --argjson providers "${providers}" \
    '{
      providers: $providers,
      cwe_image: $cwe_image,
      site_controller_vip: $site_controller_vip,
      bgp_containers_container_name: $bgp_container_name
    }' >"${out}"
}

# One Ginkgo leg. `exec_fn` runs a command against the E2E runner, which is the
# one thing that legitimately differs: CI execs into its inner runner, local
# into a container on the Kind network. Anything after `--` is appended to the
# suite's argv, which is how CI adds its extra assertions without either side
# carrying the other's concerns.
#
# The flags below are the ones both must share. `-ginkgo.fail-on-empty` is in
# that set deliberately: without it a label filter that matches no spec passes
# silently, which is the failure mode a shared filter constant exists to stop.
e2e_run_ginkgo_leg() {
  local exec_fn="$1" log_fn="$2" state_dir="$3"
  local label_filter="$4" cp_flag="$5" suffix="$6"
  shift 6
  [[ "${1:-}" == "--" ]] && shift

  "${log_fn}" "running E2E specs matching ${label_filter} (PROVISION_CONTROL_PLANE_WITH_CONTROL_PLANE_CONTROLLER=${cp_flag:-<unset>})"

  set +e
  "${exec_fn}" "${cp_flag}" \
    /state/cwe-e2e.test \
    -test.v \
    -test.timeout=105m \
    -e2e.config=/state/e2e-config.json \
    -ginkgo.v \
    -ginkgo.no-color \
    -ginkgo.fail-on-empty \
    -ginkgo.timeout=100m \
    "-ginkgo.label-filter=${label_filter}" \
    "-ginkgo.junit-report=/state/junit${suffix}.xml" \
    "$@" \
    2>&1 | tee "${state_dir}/e2e${suffix}.log"
  local rc=${PIPESTATUS[0]}
  set -e
  return "${rc}"
}

# Between the two CAPD legs the first leg's delete spec should have removed every
# tenant cluster. Count CAPI Cluster objects rather than asking the CWE API,
# which enforces auth: zero objects is an auth-free "the cluster is clean".
# Returns non-zero on timeout rather than exiting, because the two entrypoints
# want different things from that: CI dumps the surviving clusters and refuses
# the next leg, local stops outright.
e2e_wait_for_zero_tenant_clusters() {
  local kubectl_fn="$1" log_fn="$2"
  local deadline=$((SECONDS + 300)) list count
  while true; do
    if list="$("${kubectl_fn}" get clusters.cluster.x-k8s.io -A --no-headers 2>/dev/null)"; then
      count="$(printf '%s' "${list}" | grep -c . || true)"
      if [[ "${count}" == "0" ]]; then
        "${log_fn}" "cleanup gate: no tenant clusters remain"
        return 0
      fi
    fi
    if ((SECONDS >= deadline)); then
      "${log_fn}" "cleanup gate: ${count:-unknown} tenant cluster(s) still present after 5m" >&2
      return 1
    fi
    sleep 10
  done
}

# Per-node evidence: console, unit status, and the journals of the five units a
# CAPD node depends on. Without this a node whose agent never binds its port is
# undiagnosable, and the entrypoints disagreed here too: CI collected it, local
# did not.
e2e_collect_node_diagnostics() {
  local out_dir="$1" node_image="$2" node_name
  mkdir -p "${out_dir}"
  while IFS= read -r node_name; do
    [[ -n "${node_name}" ]] || continue
    docker logs "${node_name}" >"${out_dir}/${node_name}-console.log" 2>&1
    docker exec "${node_name}" systemctl status \
      nke-agent spire-agent swtpm-local containerd kubelet --no-pager \
      >"${out_dir}/${node_name}-systemd.txt" 2>&1
    docker exec "${node_name}" journalctl --no-pager \
      -u nke-agent -u spire-agent -u swtpm-local -u containerd -u kubelet \
      >"${out_dir}/${node_name}-journal.txt" 2>&1
  done < <(docker ps -a --filter "ancestor=${node_image}" --format '{{.Names}}')
}

# The Tilt providers a CAPD run needs. This belongs beside the filters because
# the two are one contract: the node-pool filter selects reboot_test.go, which
# reboots a worker through the nke.nvidia.com/reboot label, and nothing acts on
# that label unless capd-reboot-controller is deployed. The local entrypoint
# omitted it and so could never pass its own node-pool leg, on any platform.
# shellcheck disable=SC2034  # consumed by the scripts that source this file
E2E_CAPD_PROVIDERS='["capi_core", "bgp-containers", "capd", "capd-reboot-controller"]'

# The label filters for the sequential CAPD run. Named here so the two
# entrypoints cannot drift into testing different things, which they had: the
# local one ran `(capd || health) && !cp-controller` and so never ran the
# authentication specs that CI runs. authn-spire stays out because it needs
# SPIRE pod attestation that CAPD does not wire up.
# shellcheck disable=SC2034  # consumed by the scripts that source this file
E2E_CAPD_CP_CONTROLLER_FILTER='(capd && cp-controller) || health'
# shellcheck disable=SC2034
E2E_CAPD_NODE_POOL_FILTER='((capd || health) && !cp-controller) || (authn && !authn-spire) || authn-permissive'
