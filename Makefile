# setup-envtest matches the controller-runtime version in go.mod; the
# Kubernetes minor matches client-go.
ENVTEST_VERSION ?= v0.24.1
ENVTEST_K8S_VERSION ?= 1.36.x
SETUP_ENVTEST = go run sigs.k8s.io/controller-runtime/tools/setup-envtest@$(ENVTEST_VERSION)

.PHONY: build test vet lint

build:
	go build ./...

test:
	go mod download sigs.k8s.io/controller-runtime/tools/setup-envtest@$(ENVTEST_VERSION)
	KUBEBUILDER_ASSETS="$$($(SETUP_ENVTEST) use $(ENVTEST_K8S_VERSION) -p path)" go test -race ./...

vet:
	go vet ./...

lint: vet
	@out="$$(gofmt -l .)"; if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi
