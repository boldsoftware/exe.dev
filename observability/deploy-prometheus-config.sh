#!/bin/bash

scp prometheus.yml ubuntu@mon:/home/ubuntu/prometheus/prometheus.yml
ssh ubuntu@mon sudo systemctl restart prometheus
