.PHONY: deploy-grafana deploy-prometheus check-node install-node

# Check if node is installed
check-node:
	@if ! command -v node >/dev/null 2>&1; then \
		echo "Node.js is not installed. Installing..."; \
		$(MAKE) install-node; \
	fi

# Install Node.js (uses nvm if available, otherwise installs from NodeSource)
install-node:
	@if command -v nvm >/dev/null 2>&1; then \
		nvm install --lts; \
	else \
		curl -fsSL https://deb.nodesource.com/setup_lts.x | sudo -E bash -; \
		sudo apt-get install -y nodejs; \
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
	@if [ -z "$(GRAFANA_URL)" ]; then \
		echo "Error: GRAFANA_URL environment variable is not set"; \
		echo "Please set it with: export GRAFANA_URL=https://your-grafana-url/"; \
		exit 1; \
	fi
	@echo "Deploying Grafana dashboards..."
	./node_modules/.bin/tsx dashboards.mts

# Deploy Prometheus configuration
deploy-prometheus:
	@if [ -z "$(PROMETHEUS_HOST)" ]; then \
		echo "Error: PROMETHEUS_HOST environment variable is not set"; \
		echo "Please set it with: export PROMETHEUS_HOST=mon (or your prometheus server)"; \
		exit 1; \
	fi
	@if [ -z "$(PROMETHEUS_USER)" ]; then \
		PROMETHEUS_USER=ubuntu; \
	else \
		PROMETHEUS_USER=$(PROMETHEUS_USER); \
	fi; \
	echo "Deploying Prometheus configuration to $$PROMETHEUS_USER@$(PROMETHEUS_HOST)..."; \
	scp prometheus.yml $$PROMETHEUS_USER@$(PROMETHEUS_HOST):/home/$$PROMETHEUS_USER/prometheus/prometheus.yml; \
	ssh $$PROMETHEUS_USER@$(PROMETHEUS_HOST) sudo systemctl restart prometheus; \
	echo "Prometheus configuration deployed and service restarted"
