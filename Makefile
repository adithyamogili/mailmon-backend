APP_ENV ?= dev
COMPOSE_FILE = build/docker-compose.$(APP_ENV).yml

.PHONY: build run redis-up redis-down clean docker-up docker-down

build:
	go build -o mailmon ./cmd/mailmon

run: build
	APP_ENV=$(APP_ENV) ./mailmon

docker-up:
	docker compose -f $(COMPOSE_FILE) up -d

docker-down:
	docker compose -f $(COMPOSE_FILE) down

redis-up:
	docker compose -f $(COMPOSE_FILE) up -d redis

redis-down:
	docker compose -f $(COMPOSE_FILE) stop redis

clean:
	rm -f mailmon
