#!/bin/sh
# Starts a WooCommerce store on http://localhost:8089 with the official sample products,
# and prints the credentials for supplier-sync.
set -e
cd "$(dirname "$0")"
docker compose up -d
wp() { docker compose run --rm -T cli wp "$@"; }
until wp db check >/dev/null 2>&1; do sleep 2; done
wp core install --url=http://localhost:8089 --title="Demo Store" \
  --admin_user=admin --admin_password=admin --admin_email=admin@example.com --skip-email
wp plugin install woocommerce wordpress-importer --activate
wp import wp-content/plugins/woocommerce/sample-data/sample_products.xml --authors=skip
echo "export WC_STORE=http://localhost:8089 WC_USER=admin WC_PASS='$(wp user application-password create admin supplier-sync --porcelain)'"
