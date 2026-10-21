package main

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

type Rules struct {
	Markup    float64 // % added to supplier cost
	Round99   bool    // 12.30 → 12.99
	MaxChange float64 // price moves above this % go to review; 0 = off
}

func (r Rules) Price(cost float64) string {
	p := math.Round(cost*(1+r.Markup/100)*100) / 100 // 17.999999… must not floor to 17
	if r.Round99 {
		p = math.Floor(p) + 0.99
	}
	return strconv.FormatFloat(p, 'f', 2, 64)
}

type Change struct {
	P                  Product
	OldPrice, NewPrice string
	OldStock, NewStock *int
	Reason             string // why it is held for review
}

func (c Change) PriceChanged() bool { return !samePrice(c.OldPrice, c.NewPrice) }
func (c Change) StockChanged() bool {
	return c.NewStock != nil && (c.OldStock == nil || *c.OldStock != *c.NewStock)
}

type Plan struct {
	Apply, Review []Change
	Unmatched     []Item    // in feed, not in store
	Missing       []Product // in store, not in feed
	Unchanged     int
}

// buildPlan matches feed items to store products by SKU, then EAN, and works out what to change.
// Nothing is written here; the plan is what the report shows and what -apply sends.
func buildPlan(items []Item, products []Product, r Rules) Plan {
	// Variable parents hold no price or stock of their own; their variations are matched instead.
	// WooCommerce allows duplicate SKUs, so an index key can point at several products.
	bySKU, byEAN := map[string][]*Product{}, map[string][]*Product{}
	for i := range products {
		p := &products[i]
		if p.Type == "variable" {
			continue
		}
		if k := norm(p.SKU); k != "" {
			bySKU[k] = append(bySKU[k], p)
		}
		if k := norm(p.EAN); k != "" {
			byEAN[k] = append(byEAN[k], p)
		}
	}

	var pl Plan
	seen := map[int]bool{}
	for _, it := range items {
		ps := bySKU[norm(it.SKU)]
		if len(ps) == 0 {
			ps = byEAN[norm(it.EAN)]
		}
		if len(ps) == 0 {
			pl.Unmatched = append(pl.Unmatched, it)
			continue
		}
		if len(ps) > 1 {
			for _, p := range ps {
				seen[p.ID] = true
			}
			pl.Review = append(pl.Review, Change{
				P:      Product{SKU: it.SKU + it.EAN, Name: fmt.Sprintf("%d store products share this SKU/EAN", len(ps))},
				Reason: "ambiguous match",
			})
			continue
		}
		p := ps[0]
		old := p.Stock
		if p.Manage != true { // false, or "parent" where the quantity shown is the parent's
			old = nil
		}
		c := Change{P: *p, OldPrice: p.Price, NewPrice: p.Price, OldStock: old, NewStock: it.Stock}
		if it.Cost > 0 {
			c.NewPrice = r.Price(it.Cost)
		}
		move := pct(c.OldPrice, c.NewPrice)
		switch {
		case seen[p.ID]:
			c.Reason = "duplicate row in feed"
			pl.Review = append(pl.Review, c)
		case !c.PriceChanged() && !c.StockChanged():
			pl.Unchanged++
		case r.MaxChange > 0 && math.Abs(move) > r.MaxChange:
			c.Reason = fmt.Sprintf("price moves %+.0f%%", move)
			pl.Review = append(pl.Review, c)
		default:
			pl.Apply = append(pl.Apply, c)
		}
		seen[p.ID] = true
	}
	for _, p := range products {
		if !seen[p.ID] && p.Type != "variable" && p.SKU != "" {
			pl.Missing = append(pl.Missing, p)
		}
	}
	return pl
}

// Updates returns the new values to write, and the old values to restore them.
func Updates(cs []Change) (do, undo []Update) {
	t, f := true, false
	for _, c := range cs {
		u, o := Update{ID: c.P.ID, Parent: c.P.Parent}, Update{ID: c.P.ID, Parent: c.P.Parent}
		if c.PriceChanged() {
			u.Price, o.Price = &c.NewPrice, &c.OldPrice
		}
		if c.StockChanged() {
			u.Stock, u.ManageStock = c.NewStock, &t
			if c.OldStock == nil {
				o.ManageStock = &f
			} else {
				o.Stock, o.ManageStock = c.OldStock, &t
			}
		}
		do, undo = append(do, u), append(undo, o)
	}
	return do, undo
}

func norm(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

func samePrice(a, b string) bool {
	x, err1 := strconv.ParseFloat(a, 64)
	y, err2 := strconv.ParseFloat(b, 64)
	if err1 != nil || err2 != nil {
		return a == b
	}
	return math.Abs(x-y) < 0.005
}

// pct is the % move from old to new; 0 when old has no usable price.
func pct(old, new string) float64 {
	x, err1 := strconv.ParseFloat(old, 64)
	y, err2 := strconv.ParseFloat(new, 64)
	if err1 != nil || err2 != nil || x <= 0 {
		return 0
	}
	return (y - x) / x * 100
}
