#!/usr/bin/env bash

# Usage:
#   export IMMICH_API_URL="https://immich.example/api/"
#   export IMMICH_API_KEY="your-api-key"
#   ./scripts/delete-all-albums.sh
#
# Requires curl and jq. The script lists albums owned by the API-key user and
# requires typing DELETE before removing them.

set -euo pipefail

: "${IMMICH_API_URL:?Set IMMICH_API_URL, for example https://immich.example/api/}"
: "${IMMICH_API_KEY:?Set IMMICH_API_KEY before running this script}"

if ! command -v jq >/dev/null 2>&1; then
	echo "This script requires jq." >&2
	exit 1
fi

base_url="${IMMICH_API_URL%/}"
api_headers=(-H "x-api-key: ${IMMICH_API_KEY}" -H "Accept: application/json")
response_file="$(mktemp)"
trap 'rm -f "${response_file}"' EXIT

curl --compressed -fsS "${api_headers[@]}" "${base_url}/albums?isOwned=true" >"${response_file}"
if ! jq -e 'type == "array"' "${response_file}" >/dev/null; then
	echo "Immich returned an unexpected /albums response:" >&2
	head -c 500 "${response_file}" | cat -v >&2
	echo >&2
	exit 1
fi

album_count="$(jq 'length' "${response_file}")"

echo "Albums owned by the API-key user: ${album_count}"
if [[ "${album_count}" -gt 0 ]]; then
	jq -r '.[] | "- \(.albumName) [\(.id)]"' "${response_file}"
fi

read -r -p "Type DELETE to permanently delete these albums: " confirmation
if [[ "${confirmation}" != "DELETE" ]]; then
	echo "Aborted."
	exit 0
fi

jq -r '.[].id' "${response_file}" | while IFS= read -r album_id; do
	curl --compressed -fsS -X DELETE "${api_headers[@]}" "${base_url}/albums/${album_id}" >/dev/null
	echo "Deleted album ${album_id}"
done

echo "Done."
