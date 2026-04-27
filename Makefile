.PHONY: build run redis-up redis-down clean

build:
	go build -o mailmon ./cmd/mailmon

run: build
	./mailmon

redis-up:
	docker run -d --name mail-cron-redis -p 6379:6379 redis:7-alpine

redis-down:
	docker stop mail-cron-redis && docker rm mail-cron-redis

clean:
	rm -f mailmon
