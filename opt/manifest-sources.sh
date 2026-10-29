#!/usr/bin/env bash
# Branch-specific operand source map used by get_all_manifests.sh.
# Format: "repo-org:repo-name:ref-name:source-folder"
# Key is the target folder under opt/manifests/
# ref-name supports:
#   1. "branch"              — latest commit on branch (e.g., main)
#   2. "tag"                 — immutable reference (e.g., v1.0.0)
#   3. "branch@commit-sha"   — branch tracking pin (e.g., stable@a1b2c3d4)
#
# This file is not overwritten by main→stable / stable→v1.x sync-branches
# (see .github/workflows/sync-branches.yaml). Script changes in
# get_all_manifests.sh still propagate; pins and refs stay on the target branch.
# shellcheck disable=SC2034

# ODH (upstream) Component Manifests
declare -A ODH_COMPONENT_MANIFESTS=(
    ["workbenches/kf-notebook-controller"]="opendatahub-io:kubeflow:stable@a7b5c32c31600403fcbde3c1af8ee26928a9ec0e:components/notebook-controller/config"
    ["workbenches/odh-notebook-controller"]="opendatahub-io:kubeflow:stable@a7b5c32c31600403fcbde3c1af8ee26928a9ec0e:components/odh-notebook-controller/config"
    ["workbenches/notebooks"]="opendatahub-io:notebooks:stable@1f4edf1c304d5abc34be307c6be6c4be91ab211f:manifests"
    ["workbenches/workspaces-controller"]="opendatahub-io:workbenches:stable@7fa4a2bdfb11b7975903dc1583a5815acf8267a4:workspaces/controller/manifests/kustomize"
)

# RHOAI (downstream) Component Manifests
declare -A RHOAI_COMPONENT_MANIFESTS=(
    ["workbenches/kf-notebook-controller"]="red-hat-data-services:kubeflow:rhoai-3.6-ea.2@238fb6654697ec7091d97d5c05dd44cedbc12989:components/notebook-controller/config"
    ["workbenches/odh-notebook-controller"]="red-hat-data-services:kubeflow:rhoai-3.6-ea.2@238fb6654697ec7091d97d5c05dd44cedbc12989:components/odh-notebook-controller/config"
    ["workbenches/notebooks"]="red-hat-data-services:notebooks:rhoai-3.6-ea.2@b57ac85355f25ea465f43969c71f5d8bd09e7557:manifests"
    ["workbenches/workspaces-controller"]="red-hat-data-services:workbenches:rhoai-3.6-ea.2@83f98c630342561bb30df3529b04de7a2c16c8a7:workspaces/controller/manifests/kustomize"
)
