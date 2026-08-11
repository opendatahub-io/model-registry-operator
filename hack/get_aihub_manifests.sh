#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$(readlink -f "${BASH_SOURCE[0]}")")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
DST="${1:-${REPO_ROOT}/opt/manifests-template}"

echo "Destination: ${DST}"
mkdir -p "${DST}"

# --- modelregistry: copy local config/ tree ---
echo -e "\033[32mAssembling \033[33mmodelregistry\033[32m:\033[0m local config/"
mkdir -p "${DST}/modelregistry"
cp -rf "${REPO_ROOT}/config/"* "${DST}/modelregistry/"
# Remove the aihub overlay from the copy to avoid recursive self-reference
rm -rf "${DST}/modelregistry/overlays/aihub"
echo "  modelregistry: $(find "${DST}/modelregistry" -type f | wc -l) files"

# --- catalog: placeholder until EXT-1 delivers manifests ---
echo -e "\033[32mAssembling \033[33mcatalog\033[32m:\033[0m placeholder"
mkdir -p "${DST}/catalog"
cat > "${DST}/catalog/PLACEHOLDER.md" <<'EOF'
# Catalog Operator Manifests — Placeholder

The catalog operator manifests are delivered by EXT-1 (the catalog-operator owner)
and are not yet available. This directory will be populated once those manifests
are ready.
EOF
echo "  catalog: placeholder created"

echo "Done."
