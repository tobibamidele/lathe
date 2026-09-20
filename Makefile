.PHONY: build install test vet fmt-check e2e e2e-sqlite e2e-postgres e2e-mysql clean

build:
	go build -o bin/lathe ./cmd/lathe

install:
	go install ./cmd/lathe

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
