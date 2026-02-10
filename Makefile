PLUGIN_NAME ?= rainbond-plugin-template
VERSION ?= dev

.PHONY: build test vet clean check

build:
	go build -o bin/$(PLUGIN_NAME) ./cmd/plugin

# Build with public key embedded (for production)
build-with-key:
	go build -ldflags "-s -w -X main.DefaultPublicKeyPEM=$$(cat keys/public.pem)" \
		-o bin/$(PLUGIN_NAME) ./cmd/plugin

test:
	go test -v ./...

test-cover:
	go test -v -cover ./...

vet:
	go vet ./...

clean:
	rm -rf bin/

docker-build:
	docker build -t $(PLUGIN_NAME):$(VERSION) .

check: vet test build
