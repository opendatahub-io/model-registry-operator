#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$(readlink -f "${BASH_SOURCE[0]}")")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
DST="${1:-${REPO_ROOT}/opt/manifests-template}"

echo "Destination: ${DST}"
mkdir -p "${DST}"

# --- modelregistry: copy local config/ tree ---
echo -e "\033[32mAssembling \033[33mmodelregistry\033[32m:\033[0m local config/"
rm -rf "${DST}/modelregistry"
mkdir -p "${DST}/modelregistry"
cp -rf "${REPO_ROOT}/config/"* "${DST}/modelregistry/"
# Remove the aihub overlay from the copy to avoid recursive self-reference
rm -rf "${DST}/modelregistry/overlays/aihub"
echo "  modelregistry: $(find "${DST}/modelregistry" -type f | wc -l) files"

# --- catalog: delivered via the odh overlay composition ---
# Catalog manifests are included in modelregistry/overlays/odh (which composes
# ../catalog). No separate copy is needed.
echo -e "\033[32mCatalog:\033[0m delivered via modelregistry/overlays/odh composition (../catalog)"

echo "Done."
