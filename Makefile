# SPDX-License-Identifier: MIT
.PHONY: all proto build test bench e2e fmt fmt-check lint clean

GO_MODULES := go_gateway fix_bridge demo_api

all: build

proto:
	./scripts/gen_proto.sh

build: proto
	cd fxp_core && cargo build --release --locked
	cd go_gateway && go build ./...
	cd fix_bridge && go build ./...
	cd demo_api && go build ./...

test: proto
	cd fxp_core && cargo test --locked
	cd go_gateway && go test -race ./...
	cd fix_bridge && go test -race ./...
	cd demo_api && go test -race ./...

bench:
	cd fxp_core && cargo bench --bench encoding_benchmark

e2e: proto
	./scripts/e2e.sh

fmt:
	cd fxp_core && cargo fmt
	gofmt -w $(GO_MODULES)

fmt-check:
	cd fxp_core && cargo fmt --check
	@test -z "$$(gofmt -l $(GO_MODULES))" || { gofmt -l $(GO_MODULES); exit 1; }

lint:
	cd fxp_core && cargo clippy --all-targets --locked -- -D warnings
	cd go_gateway && go vet ./...
	cd fix_bridge && go vet ./...
	cd demo_api && go vet ./...
	golangci-lint run ./go_gateway/... ./fix_bridge/... ./demo_api/...

clean:
	cd fxp_core && cargo clean
	rm -rf bin target

.PHONY: dev-certs
FXP_CERTS_DIR ?= certs
dev-certs:
	bash scripts/gen_test_certs.sh "$(FXP_CERTS_DIR)"
	chmod 750 "$(FXP_CERTS_DIR)"
	chmod 640 "$(FXP_CERTS_DIR)"/*.crt "$(FXP_CERTS_DIR)/server.key" "$(FXP_CERTS_DIR)/client.key"
	@echo "Set FXP_CERT_GID=$$(id -g) for container access to these disposable certificates."

.PHONY: demo demo-down
demo:
	bash scripts/demo.sh up

demo-down:
	bash scripts/demo.sh down

.PHONY: record
record:
	cd web && npm run record

.PHONY: repro-demo
repro-demo:
	BUG="$(BUG)" bash scripts/repro_demo.sh

.PHONY: repro-blind repro-reveal
repro-blind:
	@PYTHONPATH="$(CURDIR)/examples/bot_repro$${PYTHONPATH:+:$$PYTHONPATH}" $${FXP_REPRO_PYTHON:-python3} -m gallery.blind blind
repro-reveal:
	@PYTHONPATH="$(CURDIR)/examples/bot_repro$${PYTHONPATH:+:$$PYTHONPATH}" $${FXP_REPRO_PYTHON:-python3} -m gallery.blind reveal
