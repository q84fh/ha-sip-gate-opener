IMAGE ?= ghcr.io/q84fh/ha-sip-gate-opener
TAG   ?= dev
GO    ?= go

.PHONY: test build run link image secret

test:
	$(GO) vet ./...
	$(GO) test -race -count=1 ./...

build:
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags="-s -w" -o gate-opener .

# Local preview without Home Assistant: make run, then open the link printed by `make link`
# (the webhook call will fail, but the page and its texts can be checked).
run: build
	GATE_SECRET=$$(cat .dev-secret 2>/dev/null || head -c 32 /dev/urandom | base64 | tee .dev-secret) \
	GATE_WEBHOOK_URL=http://127.0.0.1:9/ ./gate-opener serve

link: build
	@GATE_SECRET=$$(cat .dev-secret) ./gate-opener token -name "$(NAME)"

image:
	docker build -t $(IMAGE):$(TAG) .

# Prints a new random GATE_SECRET.
secret:
	@head -c 32 /dev/urandom | base64
