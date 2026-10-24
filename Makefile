# SPDX-License-Identifier: MIT
.PHONY: all proto build test bench e2e fmt fmt-check lint clean

GO_MODULES := go_gateway fix_bridge

all: build

proto:
	./scripts/gen_proto.sh

build: proto
	cd fxp_core && cargo build --release --locked
	cd go_gateway && go build ./...
	cd fix_bridge && go build ./...

test: proto
	cd fxp_core && cargo test --locked
	cd go_gateway && go test -race ./...
	cd fix_bridge && go test -race ./...

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
	cd fxp_core && cargo clippy --all-targets --locked
	cd go_gateway && go vet ./...
	cd fix_bridge && go vet ./...
	golangci-lint run ./go_gateway/... ./fix_bridge/...

clean:
	cd fxp_core && cargo clean
	rm -rf bin target
