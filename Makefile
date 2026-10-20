.PHONY: install build package test test-e2e test-container run
install:
	npm --prefix runtime ci
	npm --prefix web ci
build:
	npm --prefix runtime run build
	npm --prefix web run build
	go build -o bin/siagent ./cmd/siagent
package: build
	sh scripts/package.sh
test:
	npm --prefix runtime test
	go test -race -timeout 20m ./...
test-e2e: build
	python3 tests/e2e.py
test-container:
	docker build -t siagent:check .
	python3 tests/container.py --image siagent:check
run: build
	./bin/siagent
