BUILD_DATE := $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")
UNIX_DATE := $(shell date -u +"%s")
VCS_REF := $(shell git rev-parse HEAD)

clean:
	rm -rf bin/*

build-local: test
	@go build -o ./bin/storage-bot main.go
	@go build -o ./bin/yrfsctl cmd/yrfsctl/main.go
	@go build -o ./bin/inspectctl cmd/inspectctl/main.go

build:
	docker pull reg.deeproute.ai/deeproute-public/go/golang:alpine
	docker build --tag reg.deeproute.ai/deeproute-public/tools/storage-bot:$(VCS_REF) --build-arg "BUILD_DATE=$(BUILD_DATE)" --build-arg "VCS_REF=$(VCS_REF)" .

debug-worker:
	docker run --rm --name=storage-bot -it reg.deeproute.ai/deeproute-public/tools/storage-bot:$(VCS_REF) sh

release:
	docker tag reg.deeproute.ai/deeproute-public/tools/storage-bot:$(VCS_REF) reg.deeproute.ai/deeproute-public/tools/storage-bot:latest
	docker push reg.deeproute.ai/deeproute-public/tools/storage-bot:latest

push-dev:
	docker build --tag reg.deeproute.ai/deeproute-public/tools/storage-bot:$(UNIX_DATE) --build-arg "BUILD_DATE=$(BUILD_DATE)" --build-arg "VCS_REF=$(VCS_REF)" .
	docker push reg.deeproute.ai/deeproute-public/tools/storage-bot:$(UNIX_DATE)

test:
	go test -v `go list ./...`
