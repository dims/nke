#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Inventory, and on --apply delete, the binaries and container images this
# repository has pushed to GitHub: release assets, release records, GHCR
# packages, Actions artifacts and Actions caches. Git tags and source are never
# touched. Dry run by default; every run writes its inventory and plan to --out.

set -euo pipefail

usage() {
	cat <<'EOF'
usage: hack/github-artifacts-cleanup.sh [scope...] [filter...] [--apply [--yes]] [options]

Scopes (at least one, or --all):
  --release-assets    delete every release asset, keep the release records
  --releases          delete the release records too (drafts included); tags stay
  --packages          delete the GHCR packages this repository published
  --artifacts         delete the Actions artifacts older than --older-than days
  --caches            delete the Actions caches not used for --older-than days
  --all               every scope above

Filters (apply to release tags and asset names, package version tags,
artifact names and cache keys):
  --only REGEX        touch only items whose name matches (bash regex)
  --keep REGEX        never touch items whose name matches
  --before DATE       touch only items created before DATE (YYYY-MM-DD)
  --older-than DAYS   age floor for artifacts and caches (default 2)
  --untagged          with a filter, also delete untagged package versions

Options:
  --repo OWNER/REPO   repository (default: the checkout's GitHub remote)
  --package-prefix P  also select GHCR packages named P/... (repeatable; the
                      repository's own name and every package linked to the
                      repository are selected without this)
  --package-match RE  narrow the selected packages to names matching RE
  --apply             delete; without it the script only reports
  --yes               skip the confirmation prompt (needs --apply)
  --out DIR           where the TSV inventory, plan and results go

Deleting the last version of a package deletes the package. GitHub refuses to
delete a package with more than 5,000 downloads through the API; such rows are
reported as failed. Deleted packages can be restored for 30 days from the
package settings page; deleted releases and assets cannot.
EOF
}

repo=""
out=""
apply=false
yes=false
only=""
keep=""
before=""
older_than=2
untagged=false
pkg_match=""
scope_assets=false
scope_releases=false
scope_packages=false
scope_artifacts=false
scope_caches=false
declare -a prefixes=()

while [ $# -gt 0 ]; do
	case "$1" in
	--release-assets) scope_assets=true ;;
	--releases) scope_releases=true; scope_assets=true ;;
	--packages) scope_packages=true ;;
	--artifacts) scope_artifacts=true ;;
	--caches) scope_caches=true ;;
	--all) scope_assets=true; scope_releases=true; scope_packages=true; scope_artifacts=true; scope_caches=true ;;
	--only) only="$2"; shift ;;
	--keep) keep="$2"; shift ;;
	--before) before="$2"; shift ;;
	--older-than) older_than="$2"; shift ;;
	--untagged) untagged=true ;;
	--repo) repo="$2"; shift ;;
	--package-prefix) prefixes+=("$2"); shift ;;
	--package-match) pkg_match="$2"; shift ;;
	--apply) apply=true ;;
	--yes) yes=true ;;
	--out) out="$2"; shift ;;
	-h | --help) usage; exit 0 ;;
	*) echo "unknown argument: $1" >&2; usage >&2; exit 2 ;;
	esac
	shift
done

if ! $scope_assets && ! $scope_packages && ! $scope_artifacts && ! $scope_caches; then
	echo "pick at least one scope (or --all)" >&2
	usage >&2
	exit 2
fi
if [ -n "$before" ] && ! [[ "$before" =~ ^[0-9]{4}-[0-9]{2}-[0-9]{2}$ ]]; then
	echo "--before wants YYYY-MM-DD, got $before" >&2
	exit 2
fi
if ! [[ "$older_than" =~ ^[0-9]+$ ]]; then
	echo "--older-than wants a number of days, got $older_than" >&2
	exit 2
fi
# Artifacts and caches younger than this stay: the next run may still need them.
actions_cutoff="$(date -u -d "${older_than} days ago" +%Y-%m-%dT%H:%M:%SZ 2>/dev/null || date -u -v-"${older_than}d" +%Y-%m-%dT%H:%M:%SZ)"
for tool in gh jq; do
	command -v "$tool" >/dev/null || { echo "$tool is required" >&2; exit 1; }
done
if [ -z "$repo" ]; then
	repo="$(gh repo view --json nameWithOwner --jq .nameWithOwner)"
fi
owner="${repo%%/*}"
name="${repo#*/}"
owner_type="$(gh api "users/$owner" --jq '.type')"
case "$owner_type" in
Organization) pkg_base="orgs/$owner" ;;
User) pkg_base="users/$owner" ;;
*) echo "cannot tell whether $owner is an organization or a user" >&2; exit 1 ;;
esac
prefixes+=("$name")
out="${out:-./github-cleanup-$(date -u +%Y%m%dT%H%M%SZ)}"
mkdir -p "$out"
plan="$out/plan.tsv"
: >"$plan"

# gh api with three attempts on a 5xx; everything else fails at once.
api() {
	local attempt
	for attempt in 1 2 3; do
		if gh api "$@" 2>"$out/.api.err"; then
			return 0
		fi
		if ! grep -q -E 'HTTP 5[0-9][0-9]' "$out/.api.err"; then
			cat "$out/.api.err" >&2
			return 1
		fi
		sleep $((attempt * 5))
	done
	cat "$out/.api.err" >&2
	return 1
}

# paged ENDPOINT ITEMS FILTER: list ENDPOINT a page at a time, three attempts
# a page, and print FILTER for every element of the ITEMS array. One failed
# page must not lose the whole listing, which gh --paginate would.
paged() {
	local endpoint="$1" items="$2" filter="$3" page=1 sep='?' n attempt
	[[ "$endpoint" == *\?* ]] && sep='&'
	while :; do
		for attempt in 1 2 3; do
			if gh api "${endpoint}${sep}per_page=100&page=${page}" >"$out/.page.json" 2>"$out/.api.err"; then
				break
			fi
			if [ "$attempt" -eq 3 ]; then
				cat "$out/.api.err" >&2
				return 1
			fi
			sleep $((attempt * 5))
		done
		n=$(jq "${items} | length" "$out/.page.json")
		[ "$n" -gt 0 ] || break
		jq -r "${items}[] | ${filter}" "$out/.page.json"
		[ "$n" -eq 100 ] || break
		page=$((page + 1))
	done
}

# wanted NAME CREATED: the --only, --keep and --before filters.
wanted() {
	local item="$1" created="$2"
	if [ -n "$only" ] && ! [[ "$item" =~ $only ]]; then return 1; fi
	if [ -n "$keep" ] && [[ "$item" =~ $keep ]]; then return 1; fi
	if [ -n "$before" ] && ! [[ "${created:0:10}" < "$before" ]]; then return 1; fi
	return 0
}

filtered() {
	[ -n "$only" ] || [ -n "$keep" ] || [ -n "$before" ]
}

# plan KIND ID LABEL BYTES DELETE_PATH
add() {
	printf '%s\t%s\t%s\t%s\t%s\n' "$1" "$2" "$3" "$4" "$5" >>"$plan"
}

inventory_releases() {
	paged "repos/$repo/releases" . '[.id, .tag_name, (if .draft then "draft" else "published" end), .created_at, (.assets | length), ([.assets[].size] | add // 0)] | @tsv' >"$out/releases.tsv"
	paged "repos/$repo/releases" . '.id as $r | .tag_name as $t | .assets[] | [$r, $t, .id, .name, .size, .created_at] | @tsv' >"$out/release-assets.tsv"
	local rid tag state created count bytes aid aname asize acreated
	while IFS=$'\t' read -r rid tag aid aname asize acreated; do
		if wanted "$tag" "$acreated" || wanted "$aname" "$acreated"; then
			add asset "$aid" "$tag/$aname" "$asize" "repos/$repo/releases/assets/$aid"
		fi
	done <"$out/release-assets.tsv"
	if $scope_releases; then
		while IFS=$'\t' read -r rid tag state created _ _; do
			if wanted "$tag" "$created"; then
				add release "$rid" "$tag ($state)" 0 "repos/$repo/releases/$rid"
			fi
		done <"$out/releases.tsv"
	fi
}

inventory_packages() {
	paged "$pkg_base/packages?package_type=container" . '[.name, .visibility, (.repository.name // "-"), .created_at, .updated_at] | @tsv' >"$out/packages-all.tsv"
	: >"$out/packages.tsv"
	: >"$out/package-versions.tsv"
	local pname vis prepo pcreated pupdated prefix selected enc
	while IFS=$'\t' read -r pname vis prepo pcreated pupdated; do
		selected=false
		[ "$prepo" = "$name" ] && selected=true
		for prefix in "${prefixes[@]}"; do
			[[ "$pname" == "$prefix/"* ]] && selected=true
		done
		$selected || continue
		if [ -n "$pkg_match" ] && ! [[ "$pname" =~ $pkg_match ]]; then continue; fi
		printf '%s\t%s\t%s\t%s\t%s\n' "$pname" "$vis" "$prepo" "$pcreated" "$pupdated" >>"$out/packages.tsv"
		enc="${pname//\//%2F}"
		paged "$pkg_base/packages/container/$enc/versions" . '[.id, .created_at, ((.metadata.container.tags // []) | join(","))] | @tsv' |
			awk -F'\t' -v n="$pname" 'BEGIN { OFS = "\t" } { print n, $1, $2, $3 }' >>"$out/package-versions.tsv"
	done <"$out/packages-all.tsv"
	local vid vcreated vtags
	if filtered; then
		# Per version: untagged versions are the per-arch manifests of a kept
		# multi-arch index, so they stay unless --untagged says otherwise.
		while IFS=$'\t' read -r pname vid vcreated vtags; do
			if [ -z "$vtags" ]; then
				$untagged || continue
				wanted "" "$vcreated" || continue
			else
				wanted "$vtags" "$vcreated" || continue
			fi
			enc="${pname//\//%2F}"
			add version "$vid" "$pname:${vtags:-untagged}" 0 "$pkg_base/packages/container/$enc/versions/$vid"
		done <"$out/package-versions.tsv"
	else
		while IFS=$'\t' read -r pname vis prepo pcreated pupdated; do
			enc="${pname//\//%2F}"
			add package "$pname" "$pname ($(awk -F'\t' -v n="$pname" '$1 == n' "$out/package-versions.tsv" | wc -l | tr -d ' ') versions)" 0 "$pkg_base/packages/container/$enc"
		done <"$out/packages.tsv"
	fi
}

inventory_artifacts() {
	paged "repos/$repo/actions/artifacts" .artifacts '[.id, .name, .size_in_bytes, .expired, .created_at, .expires_at] | @tsv' >"$out/artifacts.tsv"
	local aid aname asize expired created expires
	while IFS=$'\t' read -r aid aname asize expired created _; do
		[ "$expired" = "true" ] && continue
		[[ "$created" < "$actions_cutoff" ]] || continue
		wanted "$aname" "$created" || continue
		add artifact "$aid" "$aname" "$asize" "repos/$repo/actions/artifacts/$aid"
	done <"$out/artifacts.tsv"
}

inventory_caches() {
	paged "repos/$repo/actions/caches" .actions_caches '[.id, .key, .ref, .size_in_bytes, .created_at, .last_accessed_at] | @tsv' >"$out/caches.tsv"
	local cid key ref csize created accessed
	while IFS=$'\t' read -r cid key ref csize created accessed; do
		[[ "$accessed" < "$actions_cutoff" ]] || continue
		wanted "$key" "$created" || continue
		add cache "$cid" "$key ($ref)" "$csize" "repos/$repo/actions/caches/$cid"
	done <"$out/caches.tsv"
}

echo "repository $repo ($owner_type), output in $out"
$scope_assets && inventory_releases
$scope_packages && inventory_packages
$scope_artifacts && inventory_artifacts
$scope_caches && inventory_caches

echo
echo "plan:"
awk -F'\t' '{ n[$1]++; b[$1] += $4 } END { for (k in n) printf "  %-9s %6d items %14d bytes\n", k, n[k], b[k] }' "$plan" | sort
echo "  (full list: $plan)"
if ! $apply; then
	echo
	echo "dry run; add --apply to delete"
	exit 0
fi
if [ ! -s "$plan" ]; then
	echo "nothing to delete"
	exit 0
fi
if ! $yes; then
	echo
	read -r -p "type the repository name ($repo) to delete everything in the plan: " answer
	[ "$answer" = "$repo" ] || { echo "aborted"; exit 1; }
fi

: >"$out/deleted.tsv"
: >"$out/failed.tsv"
# Assets before releases, versions before packages, so nothing is deleted twice.
for kind in asset release version package artifact cache; do
	while IFS=$'\t' read -r k id label bytes path; do
		[ "$k" = "$kind" ] || continue
		if api -X DELETE "$path" >/dev/null; then
			printf '%s\t%s\t%s\t%s\n' "$kind" "$id" "$label" "$bytes" >>"$out/deleted.tsv"
			echo "deleted $kind $label"
		else
			printf '%s\t%s\t%s\t%s\n' "$kind" "$id" "$label" "$bytes" >>"$out/failed.tsv"
			echo "FAILED  $kind $label" >&2
		fi
	done <"$plan"
done
echo
echo "deleted $(wc -l <"$out/deleted.tsv" | tr -d ' '), failed $(wc -l <"$out/failed.tsv" | tr -d ' ') (see $out)"
[ ! -s "$out/failed.tsv" ]
