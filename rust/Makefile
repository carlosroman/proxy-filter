
# Default CARGO_BIN to Cargo binary in PATH
CARGO_BIN				?= cargo

.PHONY: test
test:
	@printf '\n================================================================\n'
	@printf 'Target: test'
	@printf '\n================================================================\n'
	@($(CARGO_BIN) test)

.PHONY: build
build:
	@printf '\n================================================================\n'
	@printf 'Target: build'
	@printf '\n================================================================\n'
	@($(CARGO_BIN) build --release --all-features)

.PHONY: fmt
fmt:
	@printf '\n================================================================\n'
	@printf 'Target: fmt'
	@printf '\n================================================================\n'
	@($(CARGO_BIN) fmt)

.PHONY: lint
lint:
	@printf '\n================================================================\n'
	@printf 'Target: lint'
	@printf '\n================================================================\n'
	@($(CARGO_BIN) clippy)
