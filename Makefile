# memory-leak-reloader Makefile. The e2e target uses an ISOLATED kubeconfig and
# a dedicated kind cluster; it never touches ~/.kube/config or the current
# context.

IMG ?= ghcr.io/josegonzalez/memory-leak-reloader:dev
KIND_CLUSTER ?= memreload-e2e
KUBECONFIG_E2E ?= ./tmp/claude/$(KIND_CLUSTER).kubeconfig

.PHONY: all build test unit envtest e2e lint vet fmt docker-build helm-lint helm-docs clean generate manifests

all: build

build:
	go build -o bin/manager ./cmd

# Code generation. Pin the newest controller-tools that compiles against the
# k8s.io v0.36 apimachinery; bump if `make generate` complains about the version.
CONTROLLER_GEN ?= go run sigs.k8s.io/controller-tools/cmd/controller-gen@v0.19.0

# generate writes api/v1alpha1/zz_generated.deepcopy.go.
generate:
	$(CONTROLLER_GEN) object:headerFile=hack/boilerplate.go.txt paths=./api/...

# manifests writes the MemoryLeakPolicy CRD to the chart's files/ dir, the single
# generated source consumed by both the Helm chart and the envtest harness.
manifests:
	$(CONTROLLER_GEN) crd paths=./api/... output:crd:dir=charts/memory-leak-reloader/files

fmt:
	gofmt -w $(shell find . -name '*.go' -not -path './vendor/*')

vet:
	go vet ./...

lint: vet
	@command -v golangci-lint >/dev/null 2>&1 && golangci-lint run ./... || echo "golangci-lint not installed; ran go vet only"

# Unit tests (no cluster needed).
unit:
	go test ./... -race -count=1

# envtest-backed tests (controller-runtime test apiserver). Requires setup-envtest.
ENVTEST_K8S_VERSION ?= 1.34.0
envtest:
	@command -v setup-envtest >/dev/null 2>&1 || go install sigs.k8s.io/controller-runtime/tools/setup-envtest@latest
	KUBEBUILDER_ASSETS="$$(setup-envtest use $(ENVTEST_K8S_VERSION) -p path)" go test ./test/envtest/... -count=1

test: unit

# Full kind-based e2e with an isolated kubeconfig. The cluster is created and
# destroyed by the suite; KUBECONFIG points only at the isolated file.
e2e:
	@mkdir -p $(dir $(KUBECONFIG_E2E))
	KIND_CLUSTER=$(KIND_CLUSTER) KUBECONFIG=$(KUBECONFIG_E2E) IMG=$(IMG) \
		go test ./test/e2e/... -tags e2e -timeout 30m -count=1 -v

docker-build:
	docker build -t $(IMG) .

helm-lint:
	helm lint charts/memory-leak-reloader
	helm template memreload charts/memory-leak-reloader --set scope.mode=cluster >/dev/null

# helm-docs regenerates the chart README from README.md.gotmpl and the `# --`
# annotations in values.yaml. Pinned so local and CI output are byte-identical;
# the CI drift check reruns this target and fails on any diff.
HELM_DOCS_VERSION ?= v1.14.2
HELM_DOCS ?= go run github.com/norwoodj/helm-docs/cmd/helm-docs@$(HELM_DOCS_VERSION)

helm-docs:
	$(HELM_DOCS) --chart-search-root charts --sort-values-order file

clean:
	rm -rf bin ./tmp/claude/$(KIND_CLUSTER).kubeconfig
