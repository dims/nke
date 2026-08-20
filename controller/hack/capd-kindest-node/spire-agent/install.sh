#!/bin/bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Installs the SPIRE agent and the TPM attestor plugin into a node image.
#
# Kept in the repository so the node image can be assembled from the UPSTREAM
# PUBLIC releases of spire-agent and spire-tpm-plugin, with no internal artifact
# server needed. This mirrors what the internal bundle ships.
set -e

if [ "$EUID" -ne 0 ]; then
    echo "Error: This script must be run as root (use sudo)"
    exit 1
fi

# Get the directory where this script is located
BUNDLE_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

echo "Installing SPIRE Agent and TPM Attestor Plugin..."

BIN_DIR="/usr/local/bin"
PLUGIN_DIR="/usr/local/lib/spire-agent"
CONFIG_DIR="/etc/spire-agent"
DATA_DIR="/var/lib/spire-agent"
SYSTEMD_DIR="/etc/systemd/system"

mkdir -p "$BIN_DIR"
mkdir -p "$PLUGIN_DIR"
mkdir -p "$CONFIG_DIR"
mkdir -p "$DATA_DIR"

if ! id -u spire >/dev/null 2>&1; then
    echo "Creating spire user and group..."
    useradd -r -s /bin/false -d "$DATA_DIR" -c "SPIRE Agent" spire
fi

# Below is dependency-free way to execute the following command with no TPM libraries installed.
# tpm2_evictcontrol -C o -c 0x81010001 || true
#
# Evict handle 0x81010001 (owner hierarchy, empty auth), then close fd immediately.
# Hex command buffer:
# 8002            -> TPM_ST_SESSIONS
# 00000023        -> commandSize (35 bytes)
# 00000120        -> TPM_CC_EvictControl
# 40000001        -> auth = TPM_RH_OWNER
# 81010001        -> objectHandle (handle to evict)
# 00000009        -> authSize (9 bytes)
# 40000009        -> sessionHandle = TPM_RS_PW
# 0000000000      -> session attrs (00) + HMAC size 0000 (empty auth)
# 81010001        -> persistentHandle (same handle)
( echo '8002000000230000012040000001810100010000000940000009000000000081010001' | xxd -r -p > /dev/tpmrm0 )

chown -R spire:spire "$DATA_DIR"
chown -R spire:spire "$CONFIG_DIR"
chmod 755 "$CONFIG_DIR"

echo "Copying binaries..."
cp "$BUNDLE_DIR/bin/spire-agent" "$BIN_DIR/spire-agent"
cp "$BUNDLE_DIR/bin/tpm_attestor_agent" "$PLUGIN_DIR/tpm_attestor_agent"

chmod 755 "$BIN_DIR/spire-agent"
chmod 755 "$PLUGIN_DIR/tpm_attestor_agent"

echo "Installing systemd service..."
cp "$BUNDLE_DIR/spire-agent.service" "$SYSTEMD_DIR/spire-agent.service"
chmod 644 "$SYSTEMD_DIR/spire-agent.service"

echo "Installing environment file..."
cp "$BUNDLE_DIR/spire-agent.env" "$CONFIG_DIR/spire-agent.env"
chmod 644 "$CONFIG_DIR/spire-agent.env"
chown spire:spire "$CONFIG_DIR/spire-agent.env"

echo "Reloading systemd daemon..."
systemctl daemon-reload

echo "Enabling and starting SPIRE agent service..."
systemctl enable spire-agent
systemctl start spire-agent
