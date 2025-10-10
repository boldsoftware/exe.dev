#!/bin/sh
set -eu

go run $(go env GOROOT)/src/crypto/tls/generate_cert.go \
  --host "localhost,127.0.0.1,[::1]" \
  --ecdsa-curve P256 \
  --duration=87600h \
  --ca

openssl x509 -in cert.pem -noout -text
