// supplier-sync keeps WooCommerce prices and stock in line with a supplier feed.
// It always writes a preview report first; nothing changes in the store without -apply.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

func main() {
	feed := flag.String("feed", "", "supplier feed: file path or URL (CSV, or XML with -xml-item)")
	mapping := flag.String("map", "sku=sku,ean=ean,cost=cost,stock=stock", "feed column/tag for each field")
	xmlItem := flag.String("xml-item", "", "XML element wrapping one product, e.g. product")
	delim := flag.String("csv-delim", ",", "CSV delimiter")
	markup := flag.Float64("markup", 0, "markup % on supplier cost")
	round99 := flag.Bool("round99", false, "round prices to .99")
	maxChange := flag.Float64("max-change", 30, "hold price moves above this % for review (0 = off)")
	store := flag.String("store", os.Getenv("WC_STORE"), "store URL [WC_STORE]")
	user := flag.String("user", os.Getenv("WC_USER"), "consumer key, or WP username [WC_USER]")
	pass := flag.String("pass", os.Getenv("WC_PASS"), "consumer secret, or application password [WC_PASS]")
	report := flag.String("report", "report.html", "preview report path")
	apply := flag.Bool("apply", false, "write changes to the store (default: dry run)")
	rollback := flag.String("rollback", "", "restore a snapshot written by -apply")
	flag.Parse()

	if *store == "" {
		log.Fatal("-store is required")
	}
	woo := &Woo{Base: strings.TrimRight(*store, "/"), User: *user, Pass: *pass, HTTP: &http.Client{Timeout: time.Minute}}

	if *rollback != "" {
		var ups []Update
		b, err := os.ReadFile(*rollback)
		if err == nil {
			err = json.Unmarshal(b, &ups)
		}
		if err == nil {
			err = woo.Update(ups)
		}
		if err != nil {
			log.Fatal(err)
		}
		fmt.Printf("restored %d products from %s\n", len(ups), *rollback)
		return
	}

	if *feed == "" {
		log.Fatal("-feed is required")
	}
	m := map[string]string{}
	for _, kv := range strings.Split(*mapping, ",") {
		k, v, _ := strings.Cut(kv, "=")
		m[strings.TrimSpace(k)] = strings.TrimSpace(v)
	}
	items, skipped, err := loadFeed(*feed, m, *xmlItem, []rune(*delim)[0])
	if err != nil {
		log.Fatal(err)
	}
	products, err := woo.Products()
	if err != nil {
		log.Fatal(err)
	}
	plan := buildPlan(items, products, Rules{Markup: *markup, Round99: *round99, MaxChange: *maxChange})

	fmt.Printf("feed %d rows (%d skipped) · store %d products\n", len(items), skipped, len(products))
	fmt.Printf("update %d · review %d · unchanged %d · not in store %d · not in feed %d\n",
		len(plan.Apply), len(plan.Review), plan.Unchanged, len(plan.Unmatched), len(plan.Missing))

	if *apply && len(plan.Apply) > 0 {
		do, undo := Updates(plan.Apply)
		snap := "snapshot-" + time.Now().Format("20060102-150405") + ".json"
		b, _ := json.MarshalIndent(undo, "", "  ")
		if err := os.WriteFile(snap, b, 0o644); err != nil {
			log.Fatal(err) // no snapshot, no writes
		}
		err = woo.Update(do)
		fmt.Printf("applied %d updates · undo with: -rollback %s\n", len(do), snap)
	}
	if werr := writeReport(*report, plan, *feed, skipped, *apply); werr != nil {
		log.Fatal(werr)
	}
	fmt.Println("report:", *report)
	if err != nil {
		log.Fatal(err)
	}
}

func writeReport(path string, p Plan, feed string, skipped int, applied bool) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return reportTmpl.Execute(f, map[string]any{
		"P": p, "Feed": feed, "Skipped": skipped, "Applied": applied,
		"Time": time.Now().Format("2006-01-02 15:04"),
	})
}

var reportTmpl = template.Must(template.New("r").Funcs(template.FuncMap{
	"n": func(p *int) string {
		if p == nil {
			return "–"
		}
		return fmt.Sprint(*p)
	},
}).Parse(`<!doctype html><meta charset="utf-8"><title>Supplier sync report</title>
<style>
body{font:14px/1.5 system-ui,sans-serif;margin:24px auto;max-width:960px;padding:0 16px;color:#1f2328}
table{border-collapse:collapse;width:100%;margin:8px 0 28px}th,td{border-bottom:1px solid #d0d7de;padding:6px 8px;text-align:left}
th{background:#f6f8fa}.c{background:#fff8c5}.tag{font-size:12px;padding:2px 8px;border-radius:10px;background:#ddf4ff}
.cards{display:flex;flex-wrap:wrap;gap:8px}.cards div{border:1px solid #d0d7de;border-radius:6px;padding:8px 14px}.cards b{display:block;font-size:22px}
</style>
<h1>Supplier sync report</h1>
<p>{{.Time}} · {{.Feed}} · <span class="tag">{{if .Applied}}applied to store{{else}}dry run: nothing written{{end}}</span></p>
<div class="cards">
<div><b>{{len .P.Apply}}</b>to update</div><div><b>{{len .P.Review}}</b>held for review</div>
<div><b>{{.P.Unchanged}}</b>unchanged</div><div><b>{{len .P.Unmatched}}</b>not in store</div>
<div><b>{{len .P.Missing}}</b>not in feed</div><div><b>{{.Skipped}}</b>rows without SKU/EAN</div>
</div>
{{define "rows"}}<table><tr><th>SKU</th><th>Product</th><th>Price</th><th>Stock</th>{{if .}}{{with index . 0}}{{if .Reason}}<th>Why</th>{{end}}{{end}}{{end}}</tr>
{{range .}}<tr><td>{{.P.SKU}}</td><td>{{.P.Name}}</td>
<td{{if .PriceChanged}} class="c"{{end}}>{{.OldPrice}}{{if .PriceChanged}} → {{.NewPrice}}{{end}}</td>
<td{{if .StockChanged}} class="c"{{end}}>{{n .OldStock}}{{if .StockChanged}} → {{n .NewStock}}{{end}}</td>
{{if .Reason}}<td>{{.Reason}}</td>{{end}}</tr>{{end}}</table>{{end}}
<h2>To update</h2>{{template "rows" .P.Apply}}
<h2>Held for review</h2><p>Not written. Check them, then rerun with a higher -max-change or fix the feed.</p>{{template "rows" .P.Review}}
<h2>In feed, not in store</h2><table><tr><th>SKU</th><th>EAN</th><th>Cost</th><th>Stock</th></tr>
{{range .P.Unmatched}}<tr><td>{{.SKU}}</td><td>{{.EAN}}</td><td>{{.Cost}}</td><td>{{n .Stock}}</td></tr>{{end}}</table>
<h2>In store, not in feed</h2><p>Possibly discontinued. Left untouched.</p><table><tr><th>SKU</th><th>Product</th><th>Stock</th></tr>
{{range .P.Missing}}<tr><td>{{.SKU}}</td><td>{{.Name}}</td><td>{{n .Stock}}</td></tr>{{end}}</table>
`))
