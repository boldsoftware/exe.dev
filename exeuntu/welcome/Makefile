.PHONY: build clean stop start restart

SERVICE_NAME = welcome-exedev-webapp

build:
	go build -o welcomed ./cmd/welcomed

clean:
	rm -f welcomed

stop:
	systemctl stop $(SERVICE_NAME)

start:
	systemctl start $(SERVICE_NAME)

restart:
	systemctl restart $(SERVICE_NAME)
