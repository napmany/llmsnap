#!/bin/sh
# This script uninstalls llmsnap on Linux.
# It removes the binary, systemd service, config.yaml (optional), and llmsnap user and group.

set -eu

red="$( (/usr/bin/tput bold || :; /usr/bin/tput setaf 1 || :) 2>&-)"
plain="$( (/usr/bin/tput sgr0 || :) 2>&-)"

status() { echo ">>> $*" >&2; }
error() { echo "${red}ERROR:${plain} $*"; exit 1; }
warning() { echo "${red}WARNING:${plain} $*"; }

available() { command -v $1 >/dev/null; }

SUDO=
if [ "$(id -u)" -ne 0 ]; then
    if ! available sudo; then
        error "This script requires superuser permissions. Please re-run as root."
    fi

    SUDO="sudo"
fi

configure_systemd() {
    status "Stopping llmsnap service..."
    $SUDO systemctl stop llmsnap

    status "Disabling llmsnap service..."
    $SUDO systemctl disable llmsnap
}
if available systemctl; then
    configure_systemd
fi

if available llmsnap; then
    status "Removing llmsnap binary..."
    $SUDO rm $(which llmsnap)
fi

if [ -f "/usr/share/llmsnap/config.yaml" ]; then
    while true; do
        printf "Delete config.yaml (/usr/share/llmsnap/config.yaml)? [y/N] " >&2
        read answer
        case "$answer" in
            [Yy]* )
                $SUDO rm -r /usr/share/llmsnap
                break
                ;;
            [Nn]* | "" )
                break
                ;;
            * )
                echo "Invalid input. Please enter y or n."
                ;;
        esac
    done
fi

if id llmsnap >/dev/null 2>&1; then
    status "Removing llmsnap user..."
    $SUDO userdel llmsnap
fi

if getent group llmsnap >/dev/null 2>&1; then
    status "Removing llmsnap group..."
    $SUDO groupdel llmsnap
fi
