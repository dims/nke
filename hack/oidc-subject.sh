#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Prints the claims of the OIDC token GitHub mints for the running job, for
# the audience AWS STS expects, so the run log shows the exact subject a
# role's trust policy has to accept. The token itself is masked and never
# printed. aws-oidc-probe.yml runs this before each role assumption.
#
# Needs ACTIONS_ID_TOKEN_REQUEST_URL and ACTIONS_ID_TOKEN_REQUEST_TOKEN, which
# the runner sets when the job grants `id-token: write`, plus curl, jq and
# base64, all on the GitHub-hosted image.
set -euo pipefail

: "${ACTIONS_ID_TOKEN_REQUEST_URL:?the job needs id-token: write}"
: "${ACTIONS_ID_TOKEN_REQUEST_TOKEN:?the job needs id-token: write}"

audience="${1:-sts.amazonaws.com}"

token="$(curl -fsS -H "Authorization: bearer ${ACTIONS_ID_TOKEN_REQUEST_TOKEN}" \
  "${ACTIONS_ID_TOKEN_REQUEST_URL}&audience=${audience}" | jq -r '.value')"
if [[ -z "${token}" || "${token}" == "null" ]]; then
  echo "::error::the runner returned no OIDC token"
  exit 1
fi
echo "::add-mask::${token}"

# The payload is the second dot-separated segment, base64url without padding.
payload="$(printf '%s' "${token}" | cut -d. -f2 | tr -- '-_' '+/')"
case $(( ${#payload} % 4 )) in
  2) payload="${payload}==" ;;
  3) payload="${payload}=" ;;
esac

echo "Claims of the token this job presents for audience ${audience}:"
printf '%s' "${payload}" | base64 -d |
  jq '{sub, aud, repository, repository_id, repository_owner, repository_owner_id, environment, ref, event_name}'
