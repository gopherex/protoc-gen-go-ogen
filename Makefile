
OGEN_OPTS_DIR=$(CURDIR)/ogen
OGEN_OPTS=$(shell find "$(OGEN_OPTS_DIR)" -type f -name '*.proto')
EXAMPLE_DIR=$(CURDIR)/example
EXAMPLE_OUT_DIR=$(EXAMPLE_DIR)/gen
VALIDATE_INC=$(shell go list -m -f '{{.Dir}}' github.com/envoyproxy/protoc-gen-validate)

.PHONY: gen-opts
gen-opts:
	rm -rf ogen/*.go && easyp generate

.PHONY: build
build:
	go build -o $(CURDIR)/bin/protoc-gen-ogen ./
	go build -o $(CURDIR)/bin/protoc-gen-go google.golang.org/protobuf/cmd/protoc-gen-go
	go build -o $(CURDIR)/bin/protoc-gen-go-grpc google.golang.org/grpc/cmd/protoc-gen-go-grpc

.PHONY: gen-test
gen-test: build
	rm -rf $(EXAMPLE_OUT_DIR)
	mkdir -p $(EXAMPLE_OUT_DIR)
	protoc \
		-I $(EXAMPLE_DIR) \
		-I $(CURDIR) \
		-I $(VALIDATE_INC) \
		--plugin=protoc-gen-go=$(CURDIR)/bin/protoc-gen-go \
		--plugin=protoc-gen-go-grpc=$(CURDIR)/bin/protoc-gen-go-grpc \
		--plugin=protoc-gen-ogen=$(CURDIR)/bin/protoc-gen-ogen \
		--go_out=$(EXAMPLE_OUT_DIR) \
		--go_opt=paths=source_relative \
		--go-grpc_out=$(EXAMPLE_OUT_DIR) \
		--go-grpc_opt=paths=source_relative \
		--ogen_out=$(EXAMPLE_OUT_DIR) \
		--ogen_opt=paths=source_relative \
		--ogen_opt=ogen_config=$(EXAMPLE_DIR)/ogen.yml \
		--ogen_opt=openapi_out=$(EXAMPLE_OUT_DIR) \
		$(EXAMPLE_DIR)/golden.proto

# The plugin now invokes ogen in-process, so generation is a single protoc run.
# Kept as an alias for compatibility with earlier docs/scripts.
.PHONY: gen-ogen-test
gen-ogen-test: gen-test

.PHONY: run-test
run-test:
	go clean -testcache && go test -v ./...

.PHONY: tidy
tidy:
	go mod tidy

branch=main
.PHONY: revision
revision: # Создание тега
	@if [ -e $(tag) ]; then \
		echo "error: Specify version 'tag='"; \
		exit 1; \
	fi
	git tag -d v${tag} || true
	git push --delete origin v${tag} || true
	git tag v$(tag)
	git push origin v$(tag)
