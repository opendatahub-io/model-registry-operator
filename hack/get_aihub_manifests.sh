#!/usr/bin/env bash
set -e

GITHUB_URL="https://github.com"
SCRIPT_DIR="$(cd "$(dirname "$(readlink -f "${BASH_SOURCE[0]}")")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
DST="${1:-${REPO_ROOT}/opt/manifests-template}"

# ODH Component Manifests
# Format: "repo-org:repo-name:ref-name:source-folder"
# ref-name supports:
#   "branch"              - tracks latest commit on branch
#   "tag"                 - immutable reference
#   "branch@commit-sha"  - tracks branch but pinned to specific commit
declare -A ODH_COMPONENT_MANIFESTS=(
    ["modelregistry"]="opendatahub-io:model-registry-operator:main:config"
    ["catalog"]="opendatahub-io:model-registry-operator:main:config"
)

# RHOAI Component Manifests
declare -A RHOAI_COMPONENT_MANIFESTS=(
    ["modelregistry"]="red-hat-data-services:model-registry-operator:rhoai-next:config"
    ["catalog"]="red-hat-data-services:model-registry-operator:rhoai-next:config"
)

# Select manifests based on platform type
if [ "${ODH_PLATFORM_TYPE:-OpenDataHub}" = "OpenDataHub" ]; then
    echo "Assembling manifests for ODH"
    declare -A COMPONENT_MANIFESTS=()
    for key in "${!ODH_COMPONENT_MANIFESTS[@]}"; do
        COMPONENT_MANIFESTS["$key"]="${ODH_COMPONENT_MANIFESTS[$key]}"
    done
else
    echo "Assembling manifests for RHOAI"
    declare -A COMPONENT_MANIFESTS=()
    for key in "${!RHOAI_COMPONENT_MANIFESTS[@]}"; do
        COMPONENT_MANIFESTS["$key"]="${RHOAI_COMPONENT_MANIFESTS[$key]}"
    done
fi

# Allow overwriting repo using flags component=repo
pattern="^[a-zA-Z0-9_.-]+:[a-zA-Z0-9_.-]+:([a-zA-Z0-9_./-]+|[a-zA-Z0-9_./-]+@[a-f0-9]{7,40}):[a-zA-Z0-9_./-]+$"
if [ "$#" -ge 2 ]; then
    for arg in "${@:2}"; do
        if [[ $arg == --* ]]; then
            arg="${arg:2}"
            IFS="=" read -r key value <<< "$arg"
            if [[ -n "${COMPONENT_MANIFESTS[$key]}" ]]; then
                if [[ ! $value =~ $pattern ]]; then
                    echo "ERROR: The value '$value' does not match the expected format 'repo-org:repo-name:ref-name:source-folder'."
                    continue
                fi
                COMPONENT_MANIFESTS["$key"]=$value
            else
                echo "ERROR: '$key' does not exist in COMPONENT_MANIFESTS, it will be skipped."
                echo "Available components are: [${!COMPONENT_MANIFESTS[@]}]"
                exit 1
            fi
        fi
    done
fi

TMP_DIR=$(mktemp -d -t "aihub-manifests.XXXXXXXXXX")
trap '{ rm -rf -- "$TMP_DIR"; }' EXIT

function try_fetch_ref()
{
    local repo=$1
    local ref_type=$2
    local ref=$3

    local git_ref="refs/$ref_type/$ref"

    if git ls-remote --exit-code "$repo" "$git_ref" &>/dev/null; then
        if git fetch -q --depth 1 "$repo" "$git_ref" && git reset -q --hard FETCH_HEAD; then
            return 0
        else
            echo "ERROR: Failed to fetch $ref from $repo"
            return 1
        fi
    fi
    return 1
}

function git_fetch_ref()
{
    local repo=$1
    local ref=$2
    local dir=$3

    mkdir -p $dir
    pushd $dir &>/dev/null
    git init -q

    if [[ $ref =~ ^([a-zA-Z0-9_./-]+)@([a-f0-9]{7,40})$ ]]; then
        local commit_sha="${BASH_REMATCH[2]}"

        git remote add origin $repo
        if ! git fetch --depth 1 -q origin $commit_sha; then
            echo "ERROR: Failed to fetch from repository $repo"
            popd &>/dev/null
            return 1
        fi
        if ! git reset -q --hard $commit_sha 2>/dev/null; then
            echo "ERROR: Commit SHA $commit_sha not found in repository $repo"
            popd &>/dev/null
            return 1
        fi
    else
        if try_fetch_ref "$repo" "tags" "$ref" || try_fetch_ref "$repo" "heads" "$ref"; then
            :
        else
            echo "ERROR: '$ref' is not a valid branch, tag, or commit SHA in repository $repo"
            popd &>/dev/null
            return 1
        fi
    fi

    popd &>/dev/null
}

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
