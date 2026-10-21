# supplier-sync

Keeps WooCommerce prices and stock in line with a supplier feed, without risking the live store.

- **Any feed**: CSV (any delimiter, Excel BOM, `12,50` or `1.234,50` prices) or XML (UTF-8 / ISO-8859-1), from a file or URL
- **Matching**: by SKU, falling back to EAN/GTIN, including variations of variable products
- **No guessing**: a SKU shared by several store products is held for review, and a variation without its own SKU never matches its parent's
- **Price rules**: markup %, `.99` rounding
- **Preview first**: every run writes an HTML report; nothing changes without `-apply`
- **Safety net**: price moves above `-max-change` (default 30%) are held for review, never written
- **Undo**: `-apply` saves a snapshot of the old values; `-rollback` restores them exactly
- **Fast on big catalogs**: runs outside WordPress, writes in batches of 100 through the REST API, no plugin to install

## Usage

```sh
export WC_STORE=https://shop.example.com WC_USER=ck_... WC_PASS=cs_...

# 1. preview → report.html
supplier-sync -feed https://supplier.example.com/feed.csv -csv-delim ';' \
  -map sku=ProductCode,ean=EAN,cost=NetPrice,stock=Qty -markup 60 -round99

# 2. write it
supplier-sync ...same flags... -apply
# applied 5 updates · undo with: -rollback snapshot-20261021-155245.json

# 3. changed your mind
supplier-sync -rollback snapshot-20261021-155245.json
```

XML feeds: add `-xml-item product` (the element wrapping one product) and map child tags with `-map`.
Run it from cron for scheduled syncs.

## Try it locally

Needs Docker and Go.

```sh
./setup-demo.sh          # WooCommerce + sample products on http://localhost:8089, prints credentials
go build && ./supplier-sync -feed testdata/feed.csv -csv-delim ';' \
  -map sku=ProductCode,ean=EAN,cost=NetPrice,stock=Qty -markup 60 -round99
open report.html
```

## Not yet

Products missing from the feed are reported, not changed.
