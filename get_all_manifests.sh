#!/bin/bash
set -euo pipefail

# Workbenches module operator manifest fetching script.
# Downloads manifests from component repositories into opt/manifests/.
# Manifests are committed to the repository for hermetic container builds.
# A scheduled GitHub Action (.github/workflows/manifest-sync.yaml) refreshes them daily.
#
# Platform selection (mirrors opendatahub-operator / rhods-operator):
#   ODH_PLATFORM_TYPE=OpenDataHub  (default) — opendatahub-io upstream sources
#   ODH_PLATFORM_TYPE=<other>      — red-hat-data-services RHOAI/downstream sources
#
# Usage:
#   ./get_all_manifests.sh [--workbenches/kf-notebook-controller=org:repo:branch@sha:source_path]
#   ODH_PLATFORM_TYPE=rhoai ./get_all_manifests.sh
#
# The script clones from the specified org/repo at the given branch@sha,
# then copies source_path contents into opt/manifests/<target>.

MANIFEST_DIR="${MANIFEST_DIR:-opt/manifests}"

# {ODH,RHOAI}_COMPONENT_MANIFESTS are lists of component repositories to fetch.
# Format: "repo-org:repo-name:ref-name:source-folder"
# Key is the target folder under opt/manifests/
# ref-name supports:
#   1. "branch"              — latest commit on branch (e.g., main)
#   2. "tag"                 — immutable reference (e.g., v1.0.0)
#   3. "branch@commit-sha"   — branch tracking pin (e.g., main@a1b2c3d4)

# ODH (upstream) Component Manifests
declare -A ODH_COMPONENT_MANIFESTS=(
    ["workbenches/kf-notebook-controller"]="opendatahub-io:kubeflow:main:components/notebook-controller/config"
    ["workbenches/odh-notebook-controller"]="opendatahub-io:kubeflow:main:components/odh-notebook-controller/config"
    ["workbenches/notebooks"]="opendatahub-io:notebooks:main:manifests"
)

# RHOAI (downstream) Component Manifests
declare -A RHOAI_COMPONENT_MANIFESTS=(
    ["workbenches/kf-notebook-controller"]="red-hat-data-services:kubeflow:main:components/notebook-controller/config"
    ["workbenches/odh-notebook-controller"]="red-hat-data-services:kubeflow:main:components/odh-notebook-controller/config"
    ["workbenches/notebooks"]="red-hat-data-services:notebooks:main:manifests"
)

# Select manifests based on platform type (default: OpenDataHub / upstream)
if [ "${ODH_PLATFORM_TYPE:-OpenDataHub}" = "OpenDataHub" ]; then
    echo "Cloning manifests for ODH (upstream)"
    declare -A COMPONENT_MANIFESTS=()
    for key in "${!ODH_COMPONENT_MANIFESTS[@]}"; do
        COMPONENT_MANIFESTS["$key"]="${ODH_COMPONENT_MANIFESTS[$key]}"
    done
else
    echo "Cloning manifests for RHOAI (downstream)"
    declare -A COMPONENT_MANIFESTS=()
    for key in "${!RHOAI_COMPONENT_MANIFESTS[@]}"; do
        COMPONENT_MANIFESTS["$key"]="${RHOAI_COMPONENT_MANIFESTS[$key]}"
    done
fi

# Parse command line overrides
for arg in "$@"; do
    if [[ "${arg}" != --* ]]; then
        echo "Warning: Argument '${arg}' does not follow the '--key=value' format."
        continue
    fi
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

    if [[ -z "${org}" || -z "${repo}" || -z "${branch_sha}" || -z "${source_path}" ]]; then
        echo "ERROR: invalid spec for ${target}: '${spec}' (expected org:repo:branch[@sha]:source_path)"
        exit 1
    fi

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
        if [[ -n "${sha}" ]]; then
            # Pin to commit: shallow-fetch the SHA directly (branch name is tracking metadata).
            mkdir -p "${clone_dir}"
            (
                cd "${clone_dir}"
                git init -q
                git remote add origin "${repo_url}"
                git fetch --depth 1 -q origin "${sha}"
                git reset -q --hard "${sha}"
            )
        else
            git clone --depth 1 --branch "${branch}" "${repo_url}" "${clone_dir}"
        fi
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
