.PHONY: all server client test clean docker-server docker-client

all: server client

server:
	go build -o bin/server ./server/cmd/server

client:
	go build -o bin/client ./client/cmd/client

test:
	go test ./pkg/... ./server/... ./client/... -v -race

clean:
	rm -rf bin/

docker-server:
	docker compose -f server/docker-compose.yml build

docker-client:
	docker compose -f client/docker-compose.yml build

docker-all: docker-server docker-client
