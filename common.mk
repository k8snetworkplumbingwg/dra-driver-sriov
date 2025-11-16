# Copyright 2022 The Kubernetes Authors.
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

GOLANG_VERSION ?= 1.24.6

# Tool versions for development container
GOLANGCI_LINT_VERSION ?= v1.64.7
MOQ_VERSION ?= v0.4.0
CONTROLLER_GEN_VERSION ?= v0.14.0
CLIENT_GEN_VERSION ?= v0.29.2
MOCKGEN_VERSION ?= v0.6.0

DRIVER_NAME := dra-driver-sriov
MODULE := github.com/k8snetworkplumbingwg/$(DRIVER_NAME)

# Determine VERSION based on git state and branch
ifeq ($(VERSION),)
# Check if .git folder exists
ifeq ($(wildcard .git),)
# No .git folder - use latest
VERSION := latest
else
# Get current branch name
BRANCH_NAME := $(shell git rev-parse --abbrev-ref HEAD 2>/dev/null || echo "unknown")
ifeq ($(BRANCH_NAME),main)
# On main branch - use latest
VERSION := latest
else ifeq ($(shell echo $(BRANCH_NAME) | grep -E '^v[0-9]+\.[0-9]+\.[0-9]+.*$$'),$(BRANCH_NAME))
# Branch name looks like vX.Y.Z - use as version
VERSION := $(BRANCH_NAME)
else
# Other branches - use latest
VERSION := latest
endif
endif
endif

VENDOR := sriovnetwork.k8snetworkplumbingwg.io
APIS := virtualfunction/v1alpha1 sriovdra/v1alpha1

PLURAL_EXCEPTIONS  = DeviceClassParameters:DeviceClassParameters
PLURAL_EXCEPTIONS += ResourceSelector:ResourceSelectors

CURPATH=$(PWD)
BIN_DIR=$(CURPATH)/bin

# Default to GitHub Container Registry
ifeq ($(IMAGE_NAME),)
REGISTRY ?= ghcr.io/k8snetworkplumbingwg/$(DRIVER_NAME)
IMAGE_NAME = $(REGISTRY)
endif
