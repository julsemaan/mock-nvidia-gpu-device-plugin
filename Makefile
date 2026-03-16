IMAGE ?= ghcr.io/<owner>/mock-nvidia-gpu-device-plugin:latest

.PHONY: build test image

build:
	go build ./...

test:
	go test ./...

image:
	docker build -t $(IMAGE) .
