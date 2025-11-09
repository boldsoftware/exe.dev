.PHONY: deploy-grafana deploy-prometheus deploy-node-exporter deploy-cadvisor check-node

# Hosts to deploy node-exporter to
HOSTS := mon exed-01 exe-ctr-01

# Hosts to deploy cadvisor to
CADVISOR_HOSTS := exe-ctr-01

# Grafana configuration
GRAFANA_URL := https://grafana.crocodile-vector.ts.net/

# Check if node is installed
check-node:
	@if ! command -v node >/dev/null 2>&1; then \
		echo "Error: Node.js is not installed"; \
		echo "Please install Node.js (e.g., 'brew install node' on Mac)"; \
		exit 1; \
	fi

# Deploy Grafana dashboards
deploy-grafana: check-node
	@if [ ! -d "node_modules" ]; then \
		echo "Installing npm dependencies..."; \
		npm install; \
	fi
	@if [ -z "$(GRAFANA_BEARER_TOKEN)" ]; then \
		echo "Error: GRAFANA_BEARER_TOKEN environment variable is not set"; \
		echo "Please set it with: export GRAFANA_BEARER_TOKEN=your_token"; \
		exit 1; \
	fi
	@echo "Deploying Grafana dashboards to $(GRAFANA_URL)..."
	GRAFANA_URL=$(GRAFANA_URL) ./node_modules/.bin/tsx dashboards.mts

# Deploy Prometheus configuration
deploy-prometheus:
	@echo "Deploying Prometheus configuration to ubuntu@mon..."; \
	scp prometheus.yml ubuntu@mon:/home/ubuntu/prometheus/prometheus.yml; \
	ssh ubuntu@mon sudo systemctl restart prometheus; \
	echo "Prometheus configuration deployed and service restarted"

# Deploy node-exporter to all hosts
deploy-node-exporter:
	@for host in $(HOSTS); do \
		echo "Deploying node-exporter to $$host..."; \
		ssh ubuntu@$$host 'bash -c "if ! dpkg -l | grep -q prometheus-node-exporter; then \
			echo \"Installing prometheus-node-exporter...\"; \
			sudo apt-get update && sudo apt-get install -y prometheus-node-exporter; \
		else \
			echo \"prometheus-node-exporter already installed\"; \
		fi && \
		sudo systemctl enable prometheus-node-exporter && \
		sudo systemctl start prometheus-node-exporter && \
		echo \"Verifying node-exporter is running...\" && \
		curl -s http://localhost:9100/metrics | head -n 5"'; \
		echo ""; \
	done

# Deploy cadvisor to all hosts
deploy-cadvisor:
	@for host in $(CADVISOR_HOSTS); do \
		echo "Deploying cadvisor to $$host..."; \
		scp cadvisor.service ubuntu@$$host:/tmp/cadvisor.service; \
		ssh ubuntu@$$host 'sudo mv /tmp/cadvisor.service /etc/systemd/system/cadvisor.service && \
			sudo systemctl daemon-reload && \
			sudo systemctl enable cadvisor && \
			sudo systemctl restart cadvisor && \
			echo "Waiting for cadvisor to start..." && \
			sleep 3 && \
			echo "Verifying cadvisor is running..." && \
			curl -s http://localhost:8080/metrics | head -n 5'; \
		echo ""; \
	done
