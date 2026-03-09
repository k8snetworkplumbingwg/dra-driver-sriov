#!/bin/bash

set -ex

# github repo owner: e.g k8snetworkplumbingwg
GITHUB_REPO_OWNER=$(tr '[:upper:]' '[:lower:]' <<< "${GITHUB_REPO_OWNER:-}")
# github api token with package:write permissions
GITHUB_TOKEN=${GITHUB_TOKEN:-}
# version: tag (e.g v1.2.3) or SHA (e.g a1b2c3d)
VERSION=${VERSION:-}

BASE=${PWD}
HELM_CHART=${BASE}/deployments/helm/dra-driver-sriov

# make sure helm is installed
set +e
which helm
if [ $? -ne 0 ]; then
    echo "ERROR: helm must be installed"
    exit 1
fi
set -e

if [ -z "$GITHUB_REPO_OWNER" ]; then
    echo "ERROR: GITHUB_REPO_OWNER must be provided as env var"
    exit 1
fi

if [ -z "$GITHUB_TOKEN" ]; then
    echo "ERROR: GITHUB_TOKEN must be provided as env var"
    exit 1
fi

if [ -z "$VERSION" ]; then
    echo "ERROR: VERSION must be provided as env var"
    exit 1
fi

# Determine chart version based on VERSION format
if [[ "$VERSION" =~ ^v[0-9]+\.[0-9]+\.[0-9]+ ]]; then
    # Release tag (e.g., v1.2.3) -> chart version 1.2.3
    HELM_CHART_VERSION="${VERSION#"v"}"
else
    # Main branch SHA (e.g., a1b2c3d) -> chart version 0.0.0-a1b2c3d
    HELM_CHART_VERSION="0.0.0-${VERSION}"
fi

HELM_CHART_TARBALL="dra-driver-sriov-chart-${HELM_CHART_VERSION}.tgz"

echo "Packaging chart version: ${HELM_CHART_VERSION}"
helm package ${HELM_CHART}

echo "Logging into ghcr.io"
helm registry login ghcr.io -u ${GITHUB_REPO_OWNER} -p ${GITHUB_TOKEN}

echo "Pushing ${HELM_CHART_TARBALL} to oci://ghcr.io/${GITHUB_REPO_OWNER}"
helm push ${HELM_CHART_TARBALL} oci://ghcr.io/${GITHUB_REPO_OWNER}

# For main branch builds, also push as "latest"
if [[ ! "$VERSION" =~ ^v[0-9]+\.[0-9]+\.[0-9]+ ]]; then
    echo "Also pushing chart as 0.0.0-latest with image tag latest"
    
    BASE_DIR=${PWD}
    YQ_CMD="${BASE_DIR}/bin/yq"
    
    # Update Chart.yaml to 0.0.0-latest
    ${YQ_CMD} -i ".version = \"0.0.0-latest\"" ${HELM_CHART}/Chart.yaml
    
    # Update image tag to "latest" (aligns with container image latest tag)
    ${YQ_CMD} -i ".image.tag = \"latest\"" ${HELM_CHART}/values.yaml
    
    # Package with latest version
    HELM_CHART_TARBALL_LATEST="dra-driver-sriov-chart-0.0.0-latest.tgz"
    helm package ${HELM_CHART}
    
    # Push latest version
    echo "Pushing ${HELM_CHART_TARBALL_LATEST} to oci://ghcr.io/${GITHUB_REPO_OWNER}"
    helm push ${HELM_CHART_TARBALL_LATEST} oci://ghcr.io/${GITHUB_REPO_OWNER}
    
    echo "Successfully pushed chart version 0.0.0-latest with image tag latest"
fi
