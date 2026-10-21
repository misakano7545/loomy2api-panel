.PHONY: build run test vet fmt lint clean docker docker-run tidy

BIN      := loo2api
VERSION  ?= dev
LDFLAGS  := -s -w -X main.appVersion=$(VERSION)

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN) ./cmd/server

run: build
	./$(BIN) -config config.json

# 全量检查：格式 + 静态分析 + 单测
test:
	go test ./...

vet:
	go vet ./...

fmt:
	gofmt -l .

lint:
	gofmt -l .
	go vet ./...
	go test ./...

clean:
	rm -f $(BIN) $(BIN).exe

tidy:
	go mod tidy

docker:
	docker build --build-arg VERSION=$(VERSION) -t loomy2api-panel:$(VERSION) .

docker-run:
	docker compose up -d
