.PHONY: deploy-grafana deploy-prometheus deploy-node-exporter check-node

# Hosts to deploy node-exporter to
HOSTS := mon exed-02 exe-ctr-02 ci.crocodile-vector.ts.net exed-staging-01 exe-ctr-staging-01

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
# Note: exed-01 uses port 19100 because exed listens on most ports for its proxy functionality
deploy-node-exporter:
	@for host in $(HOSTS); do \
		echo "Deploying node-exporter to $$host..."; \
		if [ "$$host" = "exed-02" ] || [ "$$host" = "exed-staging-01" ]; then \
			port=19100; \
		else \
			port=9100; \
		fi; \
		if [ "$$host" = "ci.crocodile-vector.ts.net" ]; then \
			user=root; \
		else \
			user=ubuntu; \
		fi; \
		ssh $$user@$$host "bash -c \"if ! dpkg -l | grep -q prometheus-node-exporter; then \
			echo 'Installing prometheus-node-exporter...'; \
			sudo apt-get update && sudo apt-get install -y prometheus-node-exporter; \
		else \
			echo 'prometheus-node-exporter already installed'; \
		fi && \
		sudo mkdir -p /etc/systemd/system/prometheus-node-exporter.service.d && \
		echo '[Service]' | sudo tee /etc/systemd/system/prometheus-node-exporter.service.d/override.conf > /dev/null && \
		echo 'ExecStart=' | sudo tee -a /etc/systemd/system/prometheus-node-exporter.service.d/override.conf > /dev/null && \
		echo 'ExecStart=/usr/bin/prometheus-node-exporter --web.listen-address=:$$port --collector.cgroups --collector.systemd' | sudo tee -a /etc/systemd/system/prometheus-node-exporter.service.d/override.conf > /dev/null && \
		sudo systemctl daemon-reload && \
		sudo systemctl enable prometheus-node-exporter && \
		sudo systemctl restart prometheus-node-exporter && \
		echo 'Verifying node-exporter is running...' && \
		curl -s http://localhost:$$port/metrics | head -n 5\""; \
		echo ""; \
	done

