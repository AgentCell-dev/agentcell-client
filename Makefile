SHELL := /bin/sh
GOCACHE ?= $(CURDIR)/.cache/go-build
export GOCACHE

.PHONY: test lint lint-selftest boundary boundary-selftest release verify clean

test:
	go test ./...

lint:
	@test -z "$$(gofmt -l .)" || { echo "FAIL: gofmt required"; gofmt -l .; exit 1; }
	go vet ./...
	$(MAKE) boundary

boundary:
	@scripts/check-public-deps.sh .

boundary-selftest:
	@scripts/public-deps-selftest.sh

lint-selftest: boundary-selftest
	@go test ./internal/mcp -run 'TestAddingOneDefinitionAddsCLIAndMCP' -count=1
	@go test ./internal/cli -run 'TestFailingDeployIsAgentLegible' -count=1
	@go test ./token -run 'TestWriteReassertsPermissionsForIdenticalContent' -count=1

release:
	@test -n "$(VERSION)" || { echo "VERSION is required"; exit 2; }
	@scripts/release.sh "$(VERSION)" dist

# The adversarial harness in verification/ — mutation checks on the client's own tests, the
# reproducible-release proof, and the private-dependency probe. It moved in here from a sibling
# directory `client-verification/` with the repository split on 11 August 2026, and the reason it
# is PUBLIC is the same reason this module is: §7.1 obliges us to publish "reproducible,
# checksummed and signed" releases, and a reproducibility claim a user cannot run is a claim we are
# asking them to take on faith. `release_checks.sh` is exactly how they check it.
#
# Not part of `lint`, deliberately: `release_checks.sh` compares against dist/SHA256SUMS, which is
# gitignored build output, so it needs `make release VERSION=0.0.0-test` first and cannot pass in a
# fresh clone. Wiring it into lint would make a fresh clone fail for a reason that is not a defect.
verify:
	@sh verification/mutant_checks.sh
	@sh verification/dependency_gate_probe.sh
	@echo "note: release_checks.sh needs dist/ — run 'make release VERSION=0.0.0-test' then:"
	@echo "      sh verification/release_checks.sh"

clean:
	rm -rf .cache dist

