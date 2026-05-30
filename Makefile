.PHONY: build run fmt fmt-check vet test race ci clean

build:
	go build -o bin/argus ./cmd/argus

run: build
	./bin/argus -listen tcp:127.0.0.1:4008

fmt:
	gofmt -w .

fmt-check:
	@out=$$(gofmt -l .); if [ -n "$$out" ]; then \
		echo "unformatted files:"; echo "$$out"; exit 1; \
	fi

vet:
	go vet ./...

test:
	go test ./...

race:
	go test -race ./...

ci: fmt-check vet race build

clean:
	rm -rf bin/
