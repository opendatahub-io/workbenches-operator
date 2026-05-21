#!/bin/bash
set -euo pipefail

# Workbenches module operator manifest fetching script.
# Downloads manifests from upstream component repositories into opt/manifests/.
#
# Usage:
#   ./get_all_manifests.sh [--workbenches/kf-notebook-controller=org:repo:branch@sha:source_path]
#
# The script clones from the specified org/repo at the given branch@sha,
# then copies source_path contents into opt/manifests/<target>.

MANIFEST_DIR="${MANIFEST_DIR:-opt/manifests}"

declare -A COMPONENT_MANIFESTS=(
    ["workbenches/kf-notebook-controller"]="opendatahub-io:kubeflow:main:components/notebook-controller/config"
    ["workbenches/odh-notebook-controller"]="opendatahub-io:kubeflow:main:components/odh-notebook-controller/config"
    ["workbenches/notebooks"]="opendatahub-io:notebooks:main:manifests"
)

# Parse command line overrides
for arg in "$@"; do
    key="${arg%%=*}"
    key="${key#--}"
    value="${arg#*=}"
    if [[ -v "COMPONENT_MANIFESTS[${key}]" ]]; then
        COMPONENT_MANIFESTS["${key}"]="${value}"
    else
        echo "Unknown manifest key: ${key}"
        echo "Valid keys: ${!COMPONENT_MANIFESTS[*]}"
        exit 1
    fi
done

TMPDIR=$(mktemp -d)
trap 'rm -rf "${TMPDIR}"' EXIT

fetch_manifests() {
    local target="$1"
    local spec="$2"

    IFS=':' read -r org repo branch_sha source_path <<< "${spec}"

    local branch="${branch_sha}"
    local sha=""
    if [[ "${branch_sha}" == *"@"* ]]; then
        branch="${branch_sha%%@*}"
        sha="${branch_sha#*@}"
    fi

    local repo_url="https://github.com/${org}/${repo}.git"
    local clone_dir="${TMPDIR}/${org}-${repo}"

    echo "Fetching ${target} from ${repo_url} (branch: ${branch}, sha: ${sha:-HEAD})"

    if [[ ! -d "${clone_dir}" ]]; then
        git clone --depth 1 --branch "${branch}" "${repo_url}" "${clone_dir}" 2>/dev/null
    fi

    if [[ -n "${sha}" ]]; then
        (cd "${clone_dir}" && git fetch --depth 1 origin "${sha}" && git checkout "${sha}") 2>/dev/null
    fi

    local resolved
    resolved="$(realpath -m "${clone_dir}/${source_path}")"
    if [[ "${resolved}" != "${clone_dir}"/* ]]; then
        echo "ERROR: source_path '${source_path}' escapes clone directory"
        exit 1
    fi

    local dest="${MANIFEST_DIR}/${target}"
    mkdir -p "${dest}"
    cp -r "${resolved}/." "${dest}/"

    echo "  -> ${dest}"
}

echo "Cleaning up ${MANIFEST_DIR}..."
rm -rf "${MANIFEST_DIR:?}"
mkdir -p "${MANIFEST_DIR}"

for target in "${!COMPONENT_MANIFESTS[@]}"; do
    fetch_manifests "${target}" "${COMPONENT_MANIFESTS[${target}]}"
done

echo "All manifests fetched successfully."
