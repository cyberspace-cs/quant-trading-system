# QUANT 中高频量化交易系统 —— 常用命令
# 用法：make build / make run / make test / make smoke / make all

GO      := go
PY      := python3
ADDR    ?= :8080

.PHONY: help build run cli test smoke fmt vet py verify all

help:
	@echo "make build   编译全部 Go 包"
	@echo "make run     启动交易工作台（http://localhost$(ADDR)）"
	@echo "make cli     跑一遍 CLI 端到端演示（行情→信号→风控→执行→对账）"
	@echo "make test    Go 单元测试"
	@echo "make smoke   端到端接口冒烟（需先 make run）"
	@echo "make py      Python 回测"
	@echo "make verify  编译 + 单测 + 语法检查（CI 用）"

build:
	cd go && $(GO) build ./...

run:
	cd go && $(GO) run ./cmd/server -addr $(ADDR) -web ../web

cli:
	cd go && $(GO) run ./cmd

test:
	cd go && $(GO) test ./... -count=1

smoke:
	$(PY) scripts/smoke_test.py

fmt:
	cd go && gofmt -w .

vet:
	cd go && $(GO) vet ./...

py:
	cd python && $(PY) -m backtest.run --strategy multi_factor --top-n 8 --rebalance 5

verify: build vet test
	$(PY) -m compileall -q python && echo "python syntax OK"

all: verify
