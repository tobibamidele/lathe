.PHONY: build install test vet fmt-check version e2e e2e-sqlite e2e-postgres e2e-mysql clean

# Version and Revision are embedded into the binary and shown by `lathe version`.
# VERSION falls back from the nearest tag to the commit; REVISION is the short
# git sha. go install builds skip these and instead report the module version
# from the go toolchain's build info.
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null)
REVISION ?= $(shell git rev-parse --short HEAD 2>/dev/null)
LDFLAGS = -X github.com/tobibamidele/lathe/internal/cli.Version=$(VERSION) \
	-X github.com/tobibamidele/lathe/internal/cli.Revision=$(REVISION)

build:
	go build -ldflags "$(LDFLAGS)" -o bin/lathe ./cmd/lathe

install:
	go install -ldflags "$(LDFLAGS)" ./cmd/lathe

version: build
	./bin/lathe version

test:
	go test -race ./...

vet:
	go vet ./...

fmt-check:
	@test -z "$$(gofmt -l .)" || (echo "gofmt needed:"; gofmt -l .; exit 1)

# End-to-end runs need a live database, see integration/run.sh.
e2e-sqlite:
	./integration/run.sh sqlite

e2e-postgres:
	./integration/run.sh postgres

e2e-mysql:
	./integration/run.sh mysql

e2e: e2e-sqlite e2e-postgres e2e-mysql

clean:
	rm -rf bin integration/db integration/migrations
