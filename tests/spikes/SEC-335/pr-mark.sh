#!/bin/sh
# Post-renderer used only to measure which manifests Helm passes through a post-renderer.
sed 's/KEYCLOAK_PASSWORD/POSTRENDERED_KEYCLOAK_PASSWORD/; s/KC_BOOTSTRAP_ADMIN_PASSWORD_FILE/POSTRENDERED_KC_FILE/'
