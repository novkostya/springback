# springback — the one entrypoint.
#
# The dev host is a PURE CONTAINER HOST: no Go toolchain is installed on it. Every gate runs
# inside a pinned toolchain container built from the production Dockerfile's own build stage, so
# dev and the release image compile with identical toolchains. All pins live in versions.env.
#
# Requirements on the box: `make` + a container runtime (nerdctl or docker) with buildkit.
# Containerised gates are deliberate: the box that builds this needs `make` and a container
# runtime and nothing else — no Go, no linter, no version manager to keep in step with CI.
# The pins in versions.env are the single source of truth for both.

include versions.env

ROOT        := $(abspath .)
RUNTIME     ?= $(shell command -v nerdctl 2>/dev/null || command -v docker 2>/dev/null)
IMAGE_NAME  ?= springback
IMAGE_TAG   ?= local

TC_GO       := springback-toolchain-go:$(IMAGE_TAG)

# Named cache volumes — persistent across runs, safe to lose. They are what keep the
# containerized gates fast. Both are safe for concurrent writers: Go's build and module caches
# lock. Named for springback so they never collide with another project's on a shared box.
GO_BUILD_VOL := springback-go-build
GO_MOD_VOL   := springback-go-mod

VERSION ?= 0.0.0-dev

# Build-args threaded into every image build so the Dockerfile and the gates agree.
BUILD_ARGS := \
	--build-arg GO_IMAGE=$(GO_IMAGE) \
	--build-arg ALPINE_IMAGE=$(ALPINE_IMAGE) \
	--build-arg GOLANGCI_LINT_VERSION=$(GOLANGCI_LINT_VERSION) \
	--build-arg IPATOOL_REF=$(IPATOOL_REF) \
	--build-arg IDEVICEINSTALLER_REF=$(IDEVICEINSTALLER_REF) \
	--build-arg VERSION=$(VERSION)

# `RUN` — repo bind-mounted at /src, Go caches attached.
RUN := $(RUNTIME) run --rm -v $(ROOT):/src \
	-v $(GO_BUILD_VOL):/root/.cache/go-build -v $(GO_MOD_VOL):/go/pkg/mod

.DEFAULT_GOAL := help

.PHONY: help
help: ## Show this help
	@echo "springback (all gates run in pinned toolchain containers via $(RUNTIME)):"
	@grep -hE '^[a-zA-Z0-9_-]+:.*?## ' $(MAKEFILE_LIST) | sort | \
	  awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-12s\033[0m %s\n", $$1, $$2}'
	@echo "Runtime detected: $(RUNTIME)"

.PHONY: preflight
preflight:
	@test -n "$(RUNTIME)" || { echo "ERROR: no container runtime (nerdctl/docker) found. This box must be a container host."; exit 1; }

.PHONY: tc-go
tc-go: preflight ## Build the Go toolchain image from the Dockerfile's own stage
	$(RUNTIME) build $(BUILD_ARGS) --target toolchain-go -t $(TC_GO) -f deploy/Dockerfile .

.PHONY: gates
gates: gates-go ## Run the gate ladder

.PHONY: gates-go
gates-go: tc-go ## Go: gofmt + vet + golangci-lint + go test -race
	$(RUN) -w /src/core $(TC_GO) sh -euc '\
	    unformatted=$$(gofmt -l .); \
	    if [ -n "$$unformatted" ]; then echo "gofmt needs to run on:"; echo "$$unformatted"; exit 1; fi; \
	    go vet ./...; \
	    golangci-lint run; \
	    go test -race -cover ./...'

.PHONY: app
app: ## macOS only: assemble build/springback.app (see deploy/macos/NOTES.md)
	@test "$$(uname -s)" = "Darwin" || { echo "ERROR: make app builds a macOS bundle and must run on a Mac."; exit 1; }
	./deploy/macos/bundle.sh

.PHONY: fmt
fmt: tc-go ## Go: gofmt -w + go mod tidy (run after editing core)
	$(RUN) -w /src/core $(TC_GO) sh -euc 'gofmt -w . && go mod tidy'

.PHONY: image
image: preflight ## Build the production container (proves go:embed of the static UI)
	$(RUNTIME) build $(BUILD_ARGS) --target runtime -t $(IMAGE_NAME):$(IMAGE_TAG) -f deploy/Dockerfile .

# ---------------------------------------------------------------------------
# dev — run the built binary against the FAKE tool layer, on this box.
#
# THE FAKE IS WHY THIS TARGET EXISTS. Every external call (ipatool, ideviceinstaller, idevice_id,
# ideviceinfo, the iTunes lookup API) sits behind one interface with a fake implementation, so the
# whole app — including the at-risk detection, which is the only part that is a product rather
# than plumbing — can be exercised here, with no iPhone, no Apple ID and no netmuxd. The fake's
# device fixtures carry a genuinely-delisted app and a not-sold-in-this-storefront app, which are
# the two cases the Devices screen exists to tell apart.
# ---------------------------------------------------------------------------
# THE PORT IS TRIED, NOT ASSUMED. A dev box may already have something on this port, so
# ports on it, and 8971 was already taken by one the first time this target ran. A fixed port
# turns "somebody else is demoing" into a failed build for a reason that has nothing to do with
# the change being tested, so walk upwards until one binds.
DEV_PORT ?= 8971
DEV_APP  := springback-dev

.PHONY: dev
dev: image ## Serve this branch with the fake tool layer (no hardware needed)
	@$(RUNTIME) rm -f $(DEV_APP) >/dev/null 2>&1 || true
	@set -e; \
	port=$(DEV_PORT); \
	for _ in 1 2 3 4 5 6 7 8 9 10; do \
	  if $(RUNTIME) run -d --name $(DEV_APP) -p $$port:8971 \
	       -e SPRINGBACK_FAKE=1 -e SPRINGBACK_LIBRARY=/tmp/library -e SPRINGBACK_ACCOUNTS=/tmp/accounts \
	       $(IMAGE_NAME):$(IMAGE_TAG) serve >/dev/null 2>&1; then \
	    echo "dev: http://$(DEMO_HOST):$$port  (FAKE tools — no device, no Apple ID; stop with: make dev-stop)"; \
	    exit 0; \
	  fi; \
	  $(RUNTIME) rm -f $(DEV_APP) >/dev/null 2>&1 || true; \
	  port=$$((port + 1)); \
	done; \
	echo "dev: could not bind a port in 10 tries from $(DEV_PORT)"; exit 1

# The name to print for the dev server. Deliberately not `hostname`: the address a dev box is
# reachable at is not something to bake into a repo. Override when serving from elsewhere.
DEMO_HOST ?= localhost

.PHONY: dev-stop
dev-stop: ## Remove the dev container
	@$(RUNTIME) rm -f $(DEV_APP) >/dev/null 2>&1 || true; echo "dev: $(DEV_APP) removed"

.PHONY: push
push: preflight ## Push to $(REGISTRY) (creds via env only; never committed)
	@test -n "$(REGISTRY)" || { echo "ERROR: set REGISTRY=host[:port]/repo (env only)"; exit 1; }
	@# SAY WHICH IMAGE IS BEING PUSHED, because getting it wrong is silent and this already
	@# happened once: `make image IMAGE_TAG=staging VERSION=0.1.0` then `make push PUSH_TAG=staging`
	@# defaults IMAGE_TAG back to `local`, so it retagged the WRONG local image and shipped a
	@# 0.0.0-dev binary under the `staging` tag. Nothing in the output contradicted it — the push
	@# succeeded, the container started, and only /api/health disagreed.
	@echo "push: $(IMAGE_NAME):$(IMAGE_TAG)  ->  $(REGISTRY)/$(IMAGE_NAME):$(PUSH_TAG)"
	@$(RUNTIME) image inspect $(IMAGE_NAME):$(IMAGE_TAG) >/dev/null 2>&1 || { \
	  echo "ERROR: no local image $(IMAGE_NAME):$(IMAGE_TAG) — build it first, e.g. make image IMAGE_TAG=$(IMAGE_TAG)"; exit 1; }
	$(RUNTIME) tag  $(IMAGE_NAME):$(IMAGE_TAG) $(REGISTRY)/$(IMAGE_NAME):$(PUSH_TAG)
	$(RUNTIME) push $(REGISTRY)/$(IMAGE_NAME):$(PUSH_TAG)
PUSH_TAG ?= $(IMAGE_TAG)

.PHONY: clean
clean: ## Drop cache volumes and locally-built images
	-$(RUNTIME) volume rm $(GO_BUILD_VOL) $(GO_MOD_VOL)
	-$(RUNTIME) rmi $(TC_GO) $(IMAGE_NAME):$(IMAGE_TAG)
