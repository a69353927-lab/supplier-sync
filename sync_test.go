package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNum(t *testing.T) {
	for in, want := range map[string]float64{
		"12.50": 12.5, "12,50": 12.5, "€1.234,50": 1234.5, "1,234.50": 1234.5, "$ 7": 7,
	} {
		if got, ok := num(in); !ok || got != want {
			t.Errorf("num(%q) = %v, %v; want %v", in, got, ok, want)
		}
	}
	if _, ok := num(""); ok {
		t.Error(`num("") should be not ok`)
	}
}

func TestLatin1XML(t *testing.T) {
	src := "<?xml version=\"1.0\" encoding=\"ISO-8859-1\"?><r><p><sku>K\xf6ln-1</sku></p></r>"
	rows, err := parseXML(strings.NewReader(src), "p")
	if err != nil || rows[0]["sku"] != "Köln-1" {
		t.Fatalf("got %v, %v", rows, err)
	}
}

// End to end against a fake store: feed file → plan → batch update → rollback payload.
func TestSync(t *testing.T) {
	i := func(n int) *int { return &n }
	store := []Product{
		{ID: 1, SKU: "woo-beanie", Name: "Beanie", Price: "20", Stock: i(10), Manage: true},
		{ID: 2, SKU: "woo-belt", Name: "Belt", Price: "65", Stock: i(7), Manage: true},
		{ID: 3, SKU: "woo-cap", Name: "Cap", Price: "18", Manage: false},
		{ID: 4, SKU: "woo-sunglasses", Name: "Sunglasses", Price: "90", Stock: i(5), Manage: true},
		{ID: 5, SKU: "woo-album", Name: "Album", Price: "15", Stock: i(25), Manage: true},
		{ID: 6, SKU: "woo-tshirt", Name: "T-Shirt", Price: "18.99", Stock: i(60), Manage: true},
		{ID: 7, SKU: "woo-polo", Name: "Polo", Price: "20"},
		{ID: 8, SKU: "woo-hoodie", Name: "Hoodie", Type: "variable"},
	}
	variations := []Product{
		{ID: 81, SKU: "woo-hoodie-blue", Price: "45", Stock: i(10), Manage: "parent"}, // 10 is the parent's stock
		{ID: 82, SKU: "woo-hoodie-red", Price: "45", Manage: false},
		{ID: 83, SKU: "woo-hoodie", Price: "45"}, // no own SKU: API reports the parent's
	}
	sent := map[string][]json.RawMessage{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		route := r.URL.Query().Get("rest_route")
		switch {
		case route == "/wc/v3/products":
			json.NewEncoder(w).Encode(store)
		case route == "/wc/v3/products/8/variations":
			json.NewEncoder(w).Encode(variations)
		case strings.HasSuffix(route, "/batch"):
			var body struct{ Update []json.RawMessage }
			json.NewDecoder(r.Body).Decode(&body)
			sent[route] = body.Update
			w.Write([]byte(`{"update":[]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	woo := &Woo{Base: srv.URL, HTTP: srv.Client()}

	m := map[string]string{"sku": "ProductCode", "ean": "EAN", "cost": "NetPrice", "stock": "Qty"}
	items, skipped, err := loadFeed("testdata/feed.csv", m, "", ';')
	if err != nil || skipped != 1 || len(items) != 10 {
		t.Fatalf("feed: %d items, %d skipped, %v", len(items), skipped, err)
	}
	products, err := woo.Products()
	if err != nil {
		t.Fatal(err)
	}
	if v := products[len(products)-3]; v.Parent != 8 || v.Name != "Hoodie – " {
		t.Errorf("variation not tagged with parent: %+v", v)
	}
	p := buildPlan(items, products, Rules{Markup: 60, Round99: true, MaxChange: 30})

	// beanie 12*1.6=19.2→19.99 stock 10→40; belt 64→64.99; cap starts tracking stock at 0;
	// hoodie variations 25*1.6=40→40.99, stock tracked on the variation from now on
	if len(p.Apply) != 5 || p.Apply[0].NewPrice != "19.99" || p.Apply[1].NewPrice != "64.99" ||
		*p.Apply[2].NewStock != 0 || p.Apply[3].NewPrice != "40.99" || p.Apply[3].OldStock != nil {
		t.Errorf("apply: %s", js(p.Apply))
	}
	if len(p.Review) != 1 || p.Review[0].P.SKU != "woo-sunglasses" { // 90 → 48.99 is -46%
		t.Errorf("review: %+v", p.Review)
	}
	if p.Unchanged != 2 { // album (no price in feed, same stock), t-shirt (11.25*1.6=18→18.99)
		t.Errorf("unchanged: %d", p.Unchanged)
	}
	// ACME-999 is new; woo-vneck-tee is not here, and woo-hoodie must not match the SKU-less variation
	if len(p.Unmatched) != 2 || len(p.Missing) != 1 || p.Missing[0].SKU != "woo-polo" {
		t.Errorf("unmatched %+v missing %+v", p.Unmatched, p.Missing)
	}

	do, undo := Updates(p.Apply)
	if err := woo.Update(do); err != nil {
		t.Fatal(err)
	}
	if got := js(sent); got != `{"/wc/v3/products/8/variations/batch":[`+
		`{"id":81,"regular_price":"40.99","stock_quantity":8,"manage_stock":true},`+
		`{"id":82,"regular_price":"40.99","stock_quantity":0,"manage_stock":true}],`+
		`"/wc/v3/products/batch":[`+
		`{"id":1,"regular_price":"19.99","stock_quantity":40,"manage_stock":true},`+
		`{"id":2,"regular_price":"64.99"},`+
		`{"id":3,"regular_price":"17.99","stock_quantity":0,"manage_stock":true}]}` {
		t.Errorf("sent: %s", got)
	}
	// undo restores belt price only, turns stock tracking back off, and keeps the parent for routing
	if undo[1].Stock != nil || *undo[1].Price != "65" || *undo[2].ManageStock || *undo[3].ManageStock || undo[3].Parent != 8 {
		t.Errorf("undo: %s", js(undo))
	}
}

func TestAmbiguousSKU(t *testing.T) {
	p := buildPlan([]Item{{SKU: "dup", Cost: 5}}, []Product{{ID: 1, SKU: "dup"}, {ID: 2, SKU: "DUP "}}, Rules{})
	if len(p.Apply) != 0 || len(p.Review) != 1 || len(p.Missing) != 0 {
		t.Errorf("plan: %s", js(p))
	}
}

func js(v any) string { b, _ := json.Marshal(v); return string(b) }
