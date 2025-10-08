.PHONY: build clean stop start restart

SERVICE_NAME = welcome-exedev-webapp

build:
	go build -o welcomed ./cmd/welcomed

clean:
	rm -f welcomed

stop:
	sudo systemctl stop $(SERVICE_NAME)

start:
	sudo systemctl start $(SERVICE_NAME)

restart: build
	sudo systemctl restart $(SERVICE_NAME)
