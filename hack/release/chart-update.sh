#!/bin/bash

set -ex

# github tag e.g v1.2.3 (for releases) or short SHA (for main)
VERSION=${VERSION:-}
# github api token (needed only for read access)
GITHUB_TOKEN=${GITHUB_TOKEN:-}
# github repo owner e.g k8snetworkplumbingwg
GITHUB_REPO_OWNER=${GITHUB_REPO_OWNER:-}

BASE=${PWD}
YQ_CMD="${BASE}/bin/yq"
HELM_VALUES=${BASE}/deployments/helm/dra-driver-sriov/values.yaml
HELM_CHART=${BASE}/deployments/helm/dra-driver-sriov/Chart.yaml

if [ -z "$VERSION" ]; then
    echo "ERROR: VERSION must be provided as env var"
    exit 1
fi

if [ -z "$GITHUB_TOKEN" ]; then
    echo "ERROR: GITHUB_TOKEN must be provided as env var"
    exit 1
fi

if [ -z "$GITHUB_REPO_OWNER" ]; then
    echo "ERROR: GITHUB_REPO_OWNER must be provided as env var"
    exit 1
fi

# Determine if this is a release tag or a SHA
if [[ "$VERSION" =~ ^v[0-9]+\.[0-9]+\.[0-9]+ ]]; then
    # Release tag (e.g., v1.2.3)
    CHART_VERSION="${VERSION#"v"}"  # Remove 'v' prefix for chart version
    APP_VERSION="${VERSION}"
    IMAGE_TAG="${VERSION}"
else
    # Main branch SHA (e.g., a1b2c3d)
    CHART_VERSION="0.0.0-${VERSION}"
    APP_VERSION="${VERSION}"
    IMAGE_TAG="${VERSION}"
fi

# Update values.yaml
$YQ_CMD -i ".image.repository = \"ghcr.io/${GITHUB_REPO_OWNER}/dra-driver-sriov\"" ${HELM_VALUES}
$YQ_CMD -i ".image.tag = \"${IMAGE_TAG}\"" ${HELM_VALUES}

# Update Chart.yaml
$YQ_CMD -i ".version = \"${CHART_VERSION}\"" ${HELM_CHART}
$YQ_CMD -i ".appVersion = \"${APP_VERSION}\"" ${HELM_CHART}

echo "Chart updated:"
echo "  Chart version: ${CHART_VERSION}"
echo "  App version: ${APP_VERSION}"
echo "  Image tag: ${IMAGE_TAG}"
