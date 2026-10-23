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
    ["workbenches/kf-notebook-controller"]="opendatahub-io:kubeflow:stable@b3157f9e32a194e253f00e3d75225db711b828a1:components/notebook-controller/config"
    ["workbenches/odh-notebook-controller"]="opendatahub-io:kubeflow:stable@b3157f9e32a194e253f00e3d75225db711b828a1:components/odh-notebook-controller/config"
    ["workbenches/notebooks"]="opendatahub-io:notebooks:stable@98855910f8ab180f2558a444e3b5d4ece9c05047:manifests"
    ["workbenches/workspaces-controller"]="opendatahub-io:workbenches:stable@0ec0f512b84dc6b4ab0fe105a01029aef2ad4309:workspaces/controller/manifests/kustomize"
)

# RHOAI (downstream) Component Manifests
declare -A RHOAI_COMPONENT_MANIFESTS=(
    ["workbenches/kf-notebook-controller"]="red-hat-data-services:kubeflow:rhoai-3.6-ea.2@238fb6654697ec7091d97d5c05dd44cedbc12989:components/notebook-controller/config"
    ["workbenches/odh-notebook-controller"]="red-hat-data-services:kubeflow:rhoai-3.6-ea.2@238fb6654697ec7091d97d5c05dd44cedbc12989:components/odh-notebook-controller/config"
    ["workbenches/notebooks"]="red-hat-data-services:notebooks:rhoai-3.6-ea.2@b57ac85355f25ea465f43969c71f5d8bd09e7557:manifests"
    ["workbenches/workspaces-controller"]="red-hat-data-services:workbenches:rhoai-3.6-ea.2@83f98c630342561bb30df3529b04de7a2c16c8a7:workspaces/controller/manifests/kustomize"
)
