API_PROTO_FILES=$(shell find api -name *.proto)
REPOSITORY ?= ccr.ccs.tencentyun.com/sumery/ecommerce-gateway
GOIMAGE ?= golang:1.24.0-alpine3.21
VERSION ?= latest
GATEWAY_PORT ?= 8080
PLATFORM_1 ?= linux/amd64
PLATFORM_2 ?= linux/arm64

.PHONY: api
api:
	protoc --proto_path=./api \
	  --proto_path=./third_party \
 	  --go_out=paths=source_relative:./api \
	  $(API_PROTO_FILES)

.PHONY: dev
dev:
	CASDOOR_URL=http://apikv.com:8000 \
	CONSUL_ADDR=consul://consul.sumery.com:443 \
	CONSUL_CONFIG_PATH=ecommerce/gateway/config.yaml \
	CONSUL_CONFIG_PREFIX=ecommerce/gateway \
	POLICIES_FILE_PATH=./dynamic-config/policies/policies.csv \
	MODEL_FILE_PATH=./dynamic-config/policies/model.conf \
	USE_TLS=false \
	USE_HTTP3=false \
	HTTP_PORT=$(GATEWAY_PORT) \
	go run cmd/gateway/main.go

.PHONY: consul
consul:
	docker compose -f infrastructure/consul/compose.yaml up -d

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
