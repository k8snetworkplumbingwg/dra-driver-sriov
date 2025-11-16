# Copyright 2023 The Kubernetes Authors.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

ARG GOLANG_VERSION=undefined
ARG BASE_IMAGE=undefined

# Build stage using native architecture to avoid emulation issues during Go compilation
FROM --platform=$BUILDPLATFORM golang:${GOLANG_VERSION} AS build

ARG GOARCH=amd64

WORKDIR /go/src/github.com/k8snetworkplumbingwg/dra-driver-sriov
COPY . .

RUN mkdir /artifacts
RUN GOARCH=${GOARCH} make PREFIX=/artifacts cmds

# Final stage using target architecture
FROM --platform=linux/amd64 ${BASE_IMAGE}

LABEL org.opencontainers.image.source=https://github.com/k8snetworkplumbingwg/dra-driver-sriov
LABEL org.opencontainers.image.description="Dynamic Resource Allocation (DRA) resource driver for SR-IOV devices"
LABEL org.opencontainers.image.licenses=Apache-2.0
LABEL org.opencontainers.image.title="Dynamic Resource Allocation (DRA) resource driver for SR-IOV devices"
LABEL org.opencontainers.image.vendor="sriovnetwork.k8snetworkplumbingwg.io"
LABEL org.opencontainers.image.version=${VERSION}
LABEL org.opencontainers.image.release=${RELEASE}
LABEL io.k8s.display-name="Dynamic Resource Allocation (DRA) resource driver for SR-IOV devices"
LABEL name="Dynamic Resource Allocation (DRA) resource driver for SR-IOV devices"
LABEL vendor="sriovnetwork.k8snetworkplumbingwg.io"
ARG VERSION="N/A"
LABEL version=${VERSION}
ARG RELEASE="N/A"
LABEL release="N/A"
LABEL summary="Dynamic Resource Allocation (DRA) resource driver for SR-IOV devices"
LABEL description="See summary"

RUN yum -y install hwdata pciutils delve && yum clean all
COPY --from=build /artifacts/dra-driver-sriov /usr/bin/dra-driver-sriov
