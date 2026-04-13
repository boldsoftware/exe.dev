.PHONY: deploy-grafana deploy-prometheus deploy-node-exporter deploy-node-scripts deploy-notifications typecheck

# Hosts to deploy node-exporter to
HOSTS := mon exed-02 exe-ctr-03 exe-ctr-04 exe-ctr-05 exe-ctr-07 exe-ctr-08 exe-ctr-09 exe-ctr-10 exe-ctr-11 exe-ctr-12 exe-ctr-13 exe-ctr-14 exe-ctr-15 exe-ctr-16 exe-ctr-18 exe-ctr-19 exelet-iad-prod-01 exelet-lax2-prod-01 exelet-lax2-prod-02 exelet-lax2-prod-03 exelet-lax2-prod-04 exelet-lax-prod-03 exelet-lax-prod-04 exelet-lax-prod-05 exelet-dal-prod-01 exelet-dal-prod-02 exelet-nyc-prod-01 exelet-nyc-prod-02 exelet-nyc-prod-03 exelet-nyc-prod-04 exelet-tyo4-prod-01 exelet-tyo4-prod-02 exelet-tyo4-prod-03 exelet-lon2-prod-01 exelet-fra2-prod-01 exelet-fra2-prod-02 exelet-fra2-prod-03 exelet-syd2-prod-01 exelet-sgp-prod-01 exed-staging-01 exelet-lax2-staging-01 exelet-nyc-staging-01 exelet-pdx-staging-01 exelet-pdx-staging-02

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
	$(NODEENV)/bin/corepack enable
	$(NODEENV)/bin/pnpm install

node_modules: $(NODEENV) package.json
	$(NODEENV)/bin/pnpm install
	touch node_modules

# Run TypeScript type checking
typecheck: node_modules
	$(NODEENV)/bin/pnpm exec tsc --noEmit

# Deploy Grafana dashboards
deploy-grafana: node_modules
	@if [ -z "$(GRAFANA_BEARER_TOKEN)" ]; then \
		echo "Error: GRAFANA_BEARER_TOKEN environment variable is not set"; \
		echo "Please set it with: export GRAFANA_BEARER_TOKEN=your_token"; \
		exit 1; \
	fi
	@echo "Deploying Grafana dashboards to $(GRAFANA_URL)..."
	GRAFANA_URL=$(GRAFANA_URL) $(NODEENV)/bin/pnpm exec tsx dashboards.mts

# Deploy notification templates and contact point configs
deploy-notifications: node_modules
	@if [ -z "$(GRAFANA_BEARER_TOKEN)" ]; then \
		echo "Error: GRAFANA_BEARER_TOKEN environment variable is not set"; \
		echo "Please set it with: export GRAFANA_BEARER_TOKEN=your_token"; \
		exit 1; \
	fi
	@echo "Deploying notification templates to $(GRAFANA_URL)..."
	GRAFANA_URL=$(GRAFANA_URL) $(NODEENV)/bin/pnpm exec tsx notifications.mts

# Deploy Prometheus configuration
deploy-prometheus:
	@echo "Deploying Prometheus configuration to ubuntu@mon..."; \
	scp prometheus.yml ubuntu@mon:/home/ubuntu/prometheus/prometheus.yml; \
	ssh ubuntu@mon sudo systemctl reload prometheus; \
	echo "Prometheus configuration deployed and service restarted"

# Deploy node-exporter to all hosts (or pass specific hosts via NODES=)
# Configuration lives in deploy-node-exporter.py
deploy-node-exporter:
	python3 deploy-node-exporter.py -j $(PARALLEL) $(NODES)

# Exelet hosts (hosts with ZFS pools)
EXELET_HOSTS := exe-ctr-03 exe-ctr-04 exe-ctr-05 exe-ctr-07 exe-ctr-08 exe-ctr-09 exe-ctr-10 exe-ctr-11 exe-ctr-12 exe-ctr-13 exe-ctr-14 exe-ctr-15 exe-ctr-16 exe-ctr-18 exe-ctr-19 exelet-iad-prod-01 exelet-lax2-prod-01 exelet-lax2-prod-02 exelet-lax2-prod-03 exelet-lax2-prod-04 exelet-lax-prod-03 exelet-lax-prod-04 exelet-lax-prod-05 exelet-dal-prod-01 exelet-dal-prod-02 exelet-nyc-prod-01 exelet-nyc-prod-02 exelet-nyc-prod-03 exelet-nyc-prod-04 exelet-tyo4-prod-01 exelet-tyo4-prod-02 exelet-tyo4-prod-03 exelet-lon2-prod-01 exelet-fra2-prod-01 exelet-fra2-prod-02 exelet-fra2-prod-03 exelet-syd2-prod-01 exelet-sgp-prod-01 exelet-lax2-staging-01 exelet-nyc-staging-01 exelet-pdx-staging-01 exelet-pdx-staging-02

# Exelet hosts with replica pairs (non-pdx, non-exe-ctr)
REPLICA_EXELET_HOSTS := exelet-lax2-prod-01 exelet-lax2-prod-02 exelet-lax2-prod-03 exelet-lax2-prod-04 exelet-lax-prod-03 exelet-lax-prod-04 exelet-lax-prod-05 exelet-dal-prod-01 exelet-dal-prod-02 exelet-nyc-prod-01 exelet-nyc-prod-02 exelet-nyc-prod-03 exelet-nyc-prod-04 exelet-tyo4-prod-01 exelet-tyo4-prod-02 exelet-tyo4-prod-03 exelet-lon2-prod-01 exelet-fra2-prod-01 exelet-fra2-prod-02 exelet-fra2-prod-03 exelet-syd2-prod-01 exelet-sgp-prod-01 exelet-lax2-staging-01 exelet-nyc-staging-01

# Max parallel deployments
PARALLEL := 10

# Deploy node textfile collector scripts to hosts (parallel)
# - zpool-metrics: exelet hosts only (have ZFS pools)
# - os-updates, system-health: all hosts
deploy-node-scripts:
	@_deploy_base() { \
		host=$$1; \
		echo "[$$host] Deploying os-updates and system-health..."; \
		scp scripts/os-updates.sh scripts/system-health.sh ubuntu@$$host:/tmp/ && \
		ssh ubuntu@$$host "bash -c \" \
			sudo mkdir -p /var/lib/prometheus/node-exporter && \
			sudo mv /tmp/os-updates.sh /usr/local/bin/os-updates.sh && \
			sudo chmod +x /usr/local/bin/os-updates.sh && \
			sudo /usr/local/bin/os-updates.sh && \
			sudo mv /tmp/system-health.sh /usr/local/bin/system-health.sh && \
			sudo chmod +x /usr/local/bin/system-health.sh && \
			sudo /usr/local/bin/system-health.sh && \
			(sudo crontab -l 2>/dev/null | grep -v os-updates | grep -v system-health; \
			 echo '*/15 * * * * /usr/local/bin/os-updates.sh'; \
			 echo '* * * * * /usr/local/bin/system-health.sh') | sudo crontab -\"" && \
		echo "[$$host] os-updates and system-health done" || \
		echo "[$$host] ERROR: os-updates/system-health failed"; \
	}; \
	_deploy_zpool() { \
		host=$$1; \
		echo "[$$host] Deploying zpool-metrics..."; \
		scp scripts/zpool-metrics.sh ubuntu@$$host:/tmp/zpool-metrics.sh && \
		ssh ubuntu@$$host "bash -c \" \
			sudo mkdir -p /var/lib/prometheus/node-exporter && \
			sudo mv /tmp/zpool-metrics.sh /usr/local/bin/zpool-metrics.sh && \
			sudo chmod +x /usr/local/bin/zpool-metrics.sh && \
			sudo /usr/local/bin/zpool-metrics.sh && \
			(sudo crontab -l 2>/dev/null | grep -v zpool-metrics; \
			 echo '* * * * * /usr/local/bin/zpool-metrics.sh') | sudo crontab -\"" && \
		echo "[$$host] zpool-metrics done" || \
		echo "[$$host] ERROR: zpool-metrics failed"; \
	}; \
	_deploy_replica() { \
		host=$$1; \
		echo "[$$host] Deploying replica-dns..."; \
		scp scripts/replica-dns.sh ubuntu@$$host:/tmp/replica-dns.sh && \
		ssh ubuntu@$$host "bash -c \" \
			sudo mkdir -p /var/lib/prometheus/node-exporter && \
			sudo mv /tmp/replica-dns.sh /usr/local/bin/replica-dns.sh && \
			sudo chmod +x /usr/local/bin/replica-dns.sh && \
			sudo /usr/local/bin/replica-dns.sh && \
			(sudo crontab -l 2>/dev/null | grep -v replica-dns; \
			 echo '* * * * * /usr/local/bin/replica-dns.sh') | sudo crontab -\"" && \
		echo "[$$host] replica-dns done" || \
		echo "[$$host] ERROR: replica-dns failed"; \
	}; \
	_deploy_host() { \
		host=$$1; \
		_deploy_base $$host; \
		case " $(EXELET_HOSTS) " in *" $$host "*) _deploy_zpool $$host ;; esac; \
		case " $(REPLICA_EXELET_HOSTS) " in *" $$host "*) _deploy_replica $$host ;; esac; \
	}; \
	export -f _deploy_base _deploy_zpool _deploy_replica _deploy_host; \
	export EXELET_HOSTS="$(EXELET_HOSTS)"; \
	export REPLICA_EXELET_HOSTS="$(REPLICA_EXELET_HOSTS)"; \
	echo "Deploying node scripts to $(words $(HOSTS)) hosts ($(PARALLEL) parallel)..."; \
	printf '%s\n' $(HOSTS) | xargs -P $(PARALLEL) -I {} bash -c '_deploy_host "$$@"' _ {}

