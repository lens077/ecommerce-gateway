API_PROTO_FILES=$(shell find api -name *.proto)

.PHONY: api
# generate api proto
api:
	protoc --proto_path=./api \
	  --proto_path=./third_party \
 	  --go_out=paths=source_relative:./api \
	  $(API_PROTO_FILES)

REPOSITORY ?= harbor.apikv.com:5443/ecommerce/gateway
GOIMAGE ?= golang:1.24.0-alpine3.21
VERSION ?= latest
GATEWAY_PORT ?= 8080
PLATFORM_1 ?= linux/amd64
PLATFORM_2 ?= linux/arm64

.PHONY: dev
dev:
	CASDOOR_URL=https://apikv.com:8081 \
	DISCOVERY_DSN=consul://localhost:8500 \
	DISCOVERY_CONFIG_PATH=ecommerce/gateway/config.yaml \
	POLICIES_FILE_PATH=./dynamic-config/policies/policies.csv \
	MODEL_FILE_PATH=./dynamic-config/policies/model.conf \
	USE_TLS=false \
	USE_HTTP3=false \
	HTTP_PORT=8080 \
	go run cmd/gateway/main.go

.PHONY: run
run:
	CASDOOR_URL=https://apikv.com:8081 \
	DISCOVERY_DSN=consul://apikv.com:8500 \
	DISCOVERY_CONFIG_PATH=ecommerce/gateway/config.yaml \
	POLICIES_FILE_PATH=./dynamic-config/policies/policies.csv \
	MODEL_FILE_PATH=./dynamic-config/policies/model.conf \
	USE_TLS=false \
	USE_HTTP3=false \
	HTTP_PORT=8080 \
	go run cmd/gateway/main.go

.PHONY: k8s-dev
k8s-dev:
	kubectl apply -f deploy/dev

.PHONY: k8s-prod
k8s-prod:
	kubectl apply -f deploy/prod

.PHONY: build
build:
	docker buildx build . \
      --progress=plain \
      -t $(REPOSITORY):$(VERSION) \
      --build-arg CGOENABLED=0 \
      --build-arg GOIMAGE=$(GOIMAGE) \
      --build-arg VERSION=$(VERSION) \
      --build-arg GATEWAY_PORT=$(GATEWAY_PORT) \
      --platform $(PLATFORM_1),$(PLATFORM_2) \
      --push

https:
	chmod +x cmd/gateway/dynamic-config/tls/generate-cert.sh && cmd/gateway/dynamic-config/tls//generate-cert.sh
