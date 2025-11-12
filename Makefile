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

CONTAINER_TOOL ?= docker
MKDIR    ?= mkdir
TR       ?= tr
DIST_DIR ?= $(CURDIR)/dist
HELM     ?= "go run helm.sh/helm/v3/cmd/helm@latest"

# envtest configuration (resolved lazily in targets that need it)
ENVTEST_K8S_VERSION ?= 1.34.x
SETUP_ENVTEST ?= go run sigs.k8s.io/controller-runtime/tools/setup-envtest@latest
ENVTEST_ASSETS_DIR ?= $(BIN_DIR)/envtest

export IMAGE_GIT_TAG ?= $(shell git describe --tags --always --dirty --match 'v*' 2>/dev/null || echo 'v0.0.0-dev')
export CHART_GIT_TAG ?= $(shell git describe --tags --always --dirty --match 'chart/*' 2>/dev/null || echo 'chart/v0.0.0-dev')

include $(CURDIR)/common.mk

BUILDIMAGE_TAG ?= golang$(GOLANG_VERSION)
BUILDIMAGE ?= $(IMAGE_NAME)-build:$(BUILDIMAGE_TAG)

CMDS := $(patsubst ./cmd/%/,%,$(sort $(dir $(wildcard ./cmd/*/))))
CMD_TARGETS := $(patsubst %,cmd-%, $(CMDS))

CHECK_TARGETS := assert-fmt vet lint
MAKE_TARGETS := binaries build check vendor fmt test test-coverage cmds coverage generate mock-generate build-image $(CHECK_TARGETS)

TARGETS := $(MAKE_TARGETS) $(CMD_TARGETS)

DOCKER_TARGETS := $(patsubst %,docker-%, $(TARGETS))
.PHONY: $(TARGETS) $(DOCKER_TARGETS)

GOOS ?= linux
GOARCH ?= amd64

binaries: cmds
ifneq ($(PREFIX),)
cmd-%: COMMAND_BUILD_OPTIONS = -o $(PREFIX)/$(*)
endif
cmds: $(CMD_TARGETS)
$(CMD_TARGETS): cmd-%:
	CGO_LDFLAGS_ALLOW='-Wl,--unresolved-symbols=ignore-in-object-files' GOOS=$(GOOS) GOARCH=$(GOARCH) \
		go build -ldflags "-s -w -X main.version=$(VERSION)" $(COMMAND_BUILD_OPTIONS) $(MODULE)/cmd/$(*)
#	GOOS=$(GOOS) GOARCH=$(GOARCH) \
#    	go build -gcflags "all=-N -l" -ldflags "-X main.version=$(VERSION)" $(COMMAND_BUILD_OPTIONS) $(MODULE)/cmd/$(*)

build:
	GOOS=$(GOOS) GOARCH=$(GOARCH) go build ./...

all: check test build
check: $(CHECK_TARGETS)

# Update the vendor folder
vendor:
	go mod vendor

# Apply go fmt to the codebase
fmt:
	go list -f '{{.Dir}}' $(MODULE)/... \
		| xargs gofmt -s -l -w

assert-fmt:
	go list -f '{{.Dir}}' $(MODULE)/... \
		| xargs gofmt -s -l > fmt.out
	@if [ -s fmt.out ]; then \
		echo "\nERROR: The following files are not formatted:\n"; \
		cat fmt.out; \
		rm fmt.out; \
		exit 1; \
	else \
		rm fmt.out; \
	fi


GOLANGCI_LINT = $(BIN_DIR)/golangci-lint
lint: $(GOLANGCI_LINT)
	$(GOLANGCI_LINT) run ./cmd/... ./pkg/...

$(GOLANGCI_LINT):
	$(call go-install-tool,$(GOLANGCI_LINT),github.com/golangci/golangci-lint/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION))

vet:
	go vet $(MODULE)/...

COVERAGE_FILE := coverage.out
test:
	KUBEBUILDER_ASSETS=$$($(SETUP_ENVTEST) use $(ENVTEST_K8S_VERSION) --bin-dir=$(ENVTEST_ASSETS_DIR) -p path) go test -v -coverprofile=$(COVERAGE_FILE) $(MODULE)/...

test-coverage:
	KUBEBUILDER_ASSETS=$$($(SETUP_ENVTEST) use $(ENVTEST_K8S_VERSION) --bin-dir=$(ENVTEST_ASSETS_DIR) -p path) go test -v -covermode=atomic -coverprofile=$(COVERAGE_FILE) $(MODULE)/...

coverage: test
	cat $(COVERAGE_FILE) | grep -v "_mock.go" > $(COVERAGE_FILE).no-mocks
	go tool cover -func=$(COVERAGE_FILE).no-mocks

generate: generate-deepcopy generate-crds mock-generate

CONTROLLER_GEN = $(BIN_DIR)/controller-gen
generate-deepcopy: $(CONTROLLER_GEN)
	for api in $(APIS); do \
		rm -f $(CURDIR)/pkg/api/$${api}/zz_generated.deepcopy.go; \
		$(CONTROLLER_GEN) \
			object:headerFile=$(CURDIR)/hack/boilerplate.generatego.txt \
			paths=$(CURDIR)/pkg/api/$${api}/ \
			output:object:dir=$(CURDIR)/pkg/api/$${api}; \
	done

$(CONTROLLER_GEN):
	$(call go-install-tool,$(CONTROLLER_GEN),sigs.k8s.io/controller-tools/cmd/controller-gen@$(CONTROLLER_GEN_VERSION))

generate-crds: $(CONTROLLER_GEN)
	@mkdir -p $(CURDIR)/deployments/helm/dra-driver-sriov/templates/
	for api in $(APIS); do \
		if [ "$${api}" = "sriovdra/v1alpha1" ]; then \
			$(CONTROLLER_GEN) \
				crd \
				paths=$(CURDIR)/pkg/api/$${api}/ \
				output:crd:dir=$(CURDIR)/deployments/helm/dra-driver-sriov/templates/; \
		fi; \
	done

MISSPELL = $(BIN_DIR)/misspell
misspell: $(MISSPELL)
	$(MISSPELL) $(MODULE)/...

$(MISSPELL):
	$(call go-install-tool,$(MISSPELL),github.com/client9/misspell/cmd/misspell@latest)

MOCKGEN = $(BIN_DIR)/mockgen
gomock:
	GOBIN=$(BIN_DIR) go install go.uber.org/mock/mockgen@$(MOCKGEN_VERSION)

mock-generate: gomock
	PATH=$(BIN_DIR):$$PATH go generate ./...

$(MOCKGEN):
	$(call go-install-tool,$(MOCKGEN),go.uber.org/mock/mockgen@$(MOCKGEN_VERSION))

# Generate an image for containerized builds
# Note: This image is local only
.PHONY: .build-image
.build-image: docker/Dockerfile.devel
	if [ x"$(SKIP_IMAGE_BUILD)" = x"" ]; then \
		$(CONTAINER_TOOL) build \
			--progress=plain \
			--build-arg GOLANG_VERSION="$(GOLANG_VERSION)" \
			--build-arg GOLANGCI_LINT_VERSION="$(GOLANGCI_LINT_VERSION)" \
			--build-arg MOQ_VERSION="$(MOQ_VERSION)" \
			--build-arg CONTROLLER_GEN_VERSION="$(CONTROLLER_GEN_VERSION)" \
			--build-arg CLIENT_GEN_VERSION="$(CLIENT_GEN_VERSION)" \
			--build-arg MOCKGEN_VERSION="$(MOCKGEN_VERSION)" \
			--tag $(BUILDIMAGE) \
			-f $(^) \
			docker; \
	fi

ifeq ($(CONTAINER_TOOL),podman)
CONTAINER_TOOL_OPTS=-v $(PWD):$(PWD):Z
else
CONTAINER_TOOL_OPTS=-v $(PWD):$(PWD) --user $$(id -u):$$(id -g)
endif

$(DOCKER_TARGETS): docker-%: .build-image
	@echo "Running 'make $(*)' in container $(BUILDIMAGE)"
	$(CONTAINER_TOOL) run \
		--rm \
		-e HOME=$(PWD) \
		-e GOCACHE=$(PWD)/.cache/go \
		-e GOPATH=$(PWD)/.cache/gopath \
		$(CONTAINER_TOOL_OPTS) \
		-w $(PWD) \
		$(BUILDIMAGE) \
			make $(*)

# Start an interactive shell using the development image.
.PHONY: .shell
.shell:
	$(CONTAINER_TOOL) run \
		--rm \
		-ti \
		-e HOME=$(PWD) \
		-e GOCACHE=$(PWD)/.cache/go \
		-e GOPATH=$(PWD)/.cache/gopath \
		$(CONTAINER_TOOL_OPTS) \
		-w $(PWD) \
		$(BUILDIMAGE)

# go-install-tool will 'go install' any package $2 and install it to $1.
define go-install-tool
@[ -f $(1) ] || { \
set -e ;\
echo "Downloading $(2)" ;\
GOBIN=$(BIN_DIR) go install $(2) ;\
}
endef