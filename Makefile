API_PROTO_FILES=$(shell find api -name *.proto)
REPOSITORY ?= ccr.ccs.tencentyun.com/sumery/ecommerce-gateway
# 传给 Dockerfile 的 ARG 名是 GO_IMAGE(下划线),别写成 GOIMAGE —— 那样是空传,
# 实际用的是 Dockerfile 的默认值。这里的值必须 >= go.mod 要求的 go 版本。
GOIMAGE ?= golang:1.26.5-alpine3.22
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

.PHONY: dev-file
dev-file:
	env -u CONFIG_SOURCE_FILE \
	CONFIG_SOURCE=file \
	CONFIG_FILE=configs/config.yaml \
	go run ./cmd/gateway

.PHONY: dev
dev:
	CASDOOR_URL=https://casdoor.apikv.com \
	CONSUL_ADDR=consul://192.168.3.112:8500 \
	CONFIG_SOURCE_FILE=configs/source.dev.yaml \
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
      --build-arg GO_IMAGE=$(GOIMAGE) \
      --build-arg VERSION=$(VERSION) \
      --build-arg GATEWAY_PORT=$(GATEWAY_PORT) \
      --platform $(PLATFORM_1),$(PLATFORM_2) \
      --push
