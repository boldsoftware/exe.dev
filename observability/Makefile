.PHONY: deploy-grafana deploy-prometheus deploy-node-exporter deploy-node-scripts deploy-notifications typecheck

# Hosts to deploy node-exporter to
HOSTS := mon exed-02 exe-ctr-03 exe-ctr-04 exe-ctr-05 exe-ctr-07 exe-ctr-08 exe-ctr-09 exe-ctr-10 exe-ctr-11 exe-ctr-12 exe-ctr-13 exe-ctr-14 exe-ctr-15 exe-ctr-16 exe-ctr-18 exe-ctr-19 exelet-lax2-prod-01 exelet-lax2-prod-02 exelet-lax2-prod-03 exelet-lax2-prod-04 exelet-lax-prod-03 exelet-lax-prod-04 exelet-nyc-prod-01 exelet-nyc-prod-02 exelet-nyc-prod-03 exelet-tyo4-prod-01 exelet-tyo4-prod-02 exelet-lon2-prod-01 exelet-fra2-prod-01 exelet-fra2-prod-02 exelet-fra2-prod-03 exelet-syd2-prod-01 ci.crocodile-vector.ts.net exed-staging-01 exelet-lax2-staging-01 exelet-nyc-staging-01 exelet-pdx-staging-01 exelet-pdx-staging-02

# Dead hosts (terminated or unreachable)
DEAD_HOSTS := exe-ctr-01 exe-ctr-06 exe-ctr-staging-01 exe-ctr-staging-02 exe-ctr-02

# Grafana configuration
GRAFANA_URL := https://grafana.crocodile-vector.ts.net/

# Node environment directory
NODEENV := .nodeenv

# Ensure .nodeenv/bin is on PATH so npm's "#!/usr/bin/env node" shebang works
export PATH := $(CURDIR)/$(NODEENV)/bin:$(PATH)

# Bootstrap Node.js environment via uv nodeenv
$(NODEENV):
	uv tool run nodeenv --node=lts $(NODEENV)
	$(NODEENV)/bin/npm install

node_modules: $(NODEENV) package.json
	$(NODEENV)/bin/npm install
	touch node_modules

# Run TypeScript type checking
typecheck: node_modules
	$(NODEENV)/bin/npx tsc --noEmit

# Deploy Grafana dashboards
deploy-grafana: node_modules
	@if [ -z "$(GRAFANA_BEARER_TOKEN)" ]; then \
		echo "Error: GRAFANA_BEARER_TOKEN environment variable is not set"; \
		echo "Please set it with: export GRAFANA_BEARER_TOKEN=your_token"; \
		exit 1; \
	fi
	@echo "Deploying Grafana dashboards to $(GRAFANA_URL)..."
	GRAFANA_URL=$(GRAFANA_URL) $(NODEENV)/bin/npx tsx dashboards.mts

# Deploy notification templates and contact point configs
deploy-notifications: node_modules
	@if [ -z "$(GRAFANA_BEARER_TOKEN)" ]; then \
		echo "Error: GRAFANA_BEARER_TOKEN environment variable is not set"; \
		echo "Please set it with: export GRAFANA_BEARER_TOKEN=your_token"; \
		exit 1; \
	fi
	@echo "Deploying notification templates to $(GRAFANA_URL)..."
	GRAFANA_URL=$(GRAFANA_URL) $(NODEENV)/bin/npx tsx notifications.mts

# Deploy Prometheus configuration
deploy-prometheus:
	@echo "Deploying Prometheus configuration to ubuntu@mon..."; \
	scp prometheus.yml ubuntu@mon:/home/ubuntu/prometheus/prometheus.yml; \
	ssh ubuntu@mon sudo systemctl reload prometheus; \
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
		echo 'ExecStart=/usr/bin/prometheus-node-exporter --web.listen-address=:$$port --collector.cgroups --collector.systemd --collector.textfile --collector.textfile.directory=/var/lib/prometheus/node-exporter --collector.netdev.device-exclude='"'"'^(tap|ifb)'"'"' --collector.diskstats.device-exclude='"'"'^zd'"'"'' | sudo tee -a /etc/systemd/system/prometheus-node-exporter.service.d/override.conf > /dev/null && \
		sudo systemctl daemon-reload && \
		sudo systemctl enable prometheus-node-exporter && \
		sudo systemctl restart prometheus-node-exporter && \
		echo 'Verifying node-exporter is running...' && \
		curl -s http://localhost:$$port/metrics | head -n 5\""; \
		echo ""; \
	done

# Exelet hosts (hosts with ZFS pools)
EXELET_HOSTS := exe-ctr-03 exe-ctr-04 exe-ctr-05 exe-ctr-07 exe-ctr-08 exe-ctr-09 exe-ctr-10 exe-ctr-11 exe-ctr-12 exe-ctr-13 exe-ctr-14 exe-ctr-15 exe-ctr-16 exe-ctr-18 exe-ctr-19 exelet-lax2-prod-01 exelet-lax2-prod-02 exelet-lax2-prod-03 exelet-lax2-prod-04 exelet-lax-prod-03 exelet-lax-prod-04 exelet-nyc-prod-01 exelet-nyc-prod-02 exelet-nyc-prod-03 exelet-tyo4-prod-01 exelet-tyo4-prod-02 exelet-lon2-prod-01 exelet-fra2-prod-01 exelet-fra2-prod-02 exelet-fra2-prod-03 exelet-syd2-prod-01 exelet-lax2-staging-01 exelet-nyc-staging-01 exelet-pdx-staging-01 exelet-pdx-staging-02

# Deploy node textfile collector scripts to hosts
# - zpool-metrics: exelet hosts only (have ZFS pools)
# - os-updates, system-health: all hosts
deploy-node-scripts:
	@trap 'echo "\nInterrupted"; exit 1' INT; \
	for host in $(HOSTS); do \
		echo "Deploying os-updates and system-health to $$host..."; \
		if [ "$$host" = "ci.crocodile-vector.ts.net" ]; then \
			user=root; \
		else \
			user=ubuntu; \
		fi; \
		scp scripts/os-updates.sh scripts/system-health.sh $$user@$$host:/tmp/ && \
		ssh $$user@$$host "bash -c \" \
			sudo mkdir -p /var/lib/prometheus/node-exporter && \
			sudo mv /tmp/os-updates.sh /usr/local/bin/os-updates.sh && \
			sudo chmod +x /usr/local/bin/os-updates.sh && \
			sudo /usr/local/bin/os-updates.sh && \
			sudo mv /tmp/system-health.sh /usr/local/bin/system-health.sh && \
			sudo chmod +x /usr/local/bin/system-health.sh && \
			sudo /usr/local/bin/system-health.sh && \
			(sudo crontab -l 2>/dev/null | grep -v os-updates | grep -v system-health; \
			 echo '*/15 * * * * /usr/local/bin/os-updates.sh'; \
			 echo '* * * * * /usr/local/bin/system-health.sh') | sudo crontab - && \
			echo 'Cron jobs installed:' && sudo crontab -l | grep -E 'os-updates|system-health'\""; \
		echo ""; \
	done; \
	for host in $(EXELET_HOSTS); do \
		echo "Deploying zpool-metrics to $$host..."; \
		scp scripts/zpool-metrics.sh ubuntu@$$host:/tmp/zpool-metrics.sh && \
		ssh ubuntu@$$host "bash -c \" \
			sudo mkdir -p /var/lib/prometheus/node-exporter && \
			sudo mv /tmp/zpool-metrics.sh /usr/local/bin/zpool-metrics.sh && \
			sudo chmod +x /usr/local/bin/zpool-metrics.sh && \
			sudo /usr/local/bin/zpool-metrics.sh && \
			(sudo crontab -l 2>/dev/null | grep -v zpool-metrics; \
			 echo '* * * * * /usr/local/bin/zpool-metrics.sh') | sudo crontab - && \
			echo 'Cron job installed:' && sudo crontab -l | grep zpool-metrics\""; \
		echo ""; \
	done

