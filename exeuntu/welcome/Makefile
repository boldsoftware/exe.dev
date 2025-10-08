.PHONY: build clean stop start restart test

SERVICE_NAME = welcome

build:
	go build -o welcomed ./cmd/welcomed

clean:
	rm -f welcomed

test:
	go test ./...

# No sudo needed - polkit rule in exeuntu/welcome-manage.polkit allows exedev to manage this service
stop:
	systemctl stop $(SERVICE_NAME)

start:
	systemctl start $(SERVICE_NAME)

restart: build
	systemctl restart $(SERVICE_NAME)
