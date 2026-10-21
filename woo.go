package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// Woo talks to the WooCommerce REST API with basic auth:
// consumer key/secret over HTTPS, or a WordPress application password.
type Woo struct {
	Base, User, Pass string
	HTTP             *http.Client
}

type Product struct {
	ID     int    `json:"id"`
	Parent int    `json:"parent_id"` // set on variations
	SKU    string `json:"sku"`
	EAN    string `json:"global_unique_id"`
	Name   string `json:"name"`
	Type   string `json:"type"`
	Price  string `json:"regular_price"`
	Stock  *int   `json:"stock_quantity"`
	Manage any    `json:"manage_stock"` // true, false, or "parent" on variations
	Attrs  []struct {
		Option string `json:"option"`
	} `json:"attributes"`
}

// Update is one product's new (or, in a snapshot, old) values.
// Parent routes variations to their own batch endpoint; it is not sent to the API.
type Update struct {
	ID          int     `json:"id"`
	Parent      int     `json:"parent_id,omitempty"`
	Price       *string `json:"regular_price,omitempty"`
	Stock       *int    `json:"stock_quantity,omitempty"`
	ManageStock *bool   `json:"manage_stock,omitempty"`
}

// Products returns all products plus the variations of variable products.
func (w *Woo) Products() ([]Product, error) {
	ps, err := w.list("products", "id,sku,global_unique_id,name,type,regular_price,manage_stock,stock_quantity")
	if err != nil {
		return nil, err
	}
	all := ps
	// ponytail: one sequential request per variable product; parallelise if big catalogs get slow.
	for _, p := range ps {
		if p.Type != "variable" {
			continue
		}
		vs, err := w.list(fmt.Sprintf("products/%d/variations", p.ID),
			"id,sku,global_unique_id,regular_price,manage_stock,stock_quantity,attributes")
		if err != nil {
			return nil, err
		}
		for i := range vs {
			v := &vs[i]
			v.Type, v.Parent = "variation", p.ID
			// a variation without its own SKU/EAN reports the parent's; it must not match on it
			if v.SKU == p.SKU {
				v.SKU = ""
			}
			if v.EAN == p.EAN {
				v.EAN = ""
			}
			opts := make([]string, len(v.Attrs))
			for j, a := range v.Attrs {
				opts[j] = a.Option
			}
			v.Name = p.Name + " – " + strings.Join(opts, ", ")
		}
		all = append(all, vs...)
	}
	return all, nil
}

func (w *Woo) list(route, fields string) ([]Product, error) {
	var all []Product
	for page := 1; ; page++ {
		var batch []Product
		q := url.Values{"per_page": {"100"}, "page": {strconv.Itoa(page)}, "_fields": {fields}}
		if err := w.do("GET", route, q, nil, &batch); err != nil {
			return nil, err
		}
		all = append(all, batch...)
		if len(batch) < 100 {
			return all, nil
		}
	}
}

// Update sends changes in batches of 100 (the API limit), variations grouped under their parent,
// and keeps going past per-product errors.
func (w *Woo) Update(ups []Update) error {
	groups := map[int][]Update{}
	var parents []int
	for _, u := range ups {
		if _, ok := groups[u.Parent]; !ok {
			parents = append(parents, u.Parent)
		}
		p := u.Parent
		u.Parent = 0
		groups[p] = append(groups[p], u)
	}

	var errs []error
	for _, parent := range parents {
		route := "products/batch"
		if parent != 0 {
			route = fmt.Sprintf("products/%d/variations/batch", parent)
		}
		for g := groups[parent]; len(g) > 0; {
			n := min(len(g), 100)
			var res struct {
				Update []struct {
					ID    int `json:"id"`
					Error *struct {
						Message string `json:"message"`
					} `json:"error"`
				} `json:"update"`
			}
			if err := w.do("POST", route, nil, map[string]any{"update": g[:n]}, &res); err != nil {
				errs = append(errs, err)
			}
			for _, r := range res.Update {
				if r.Error != nil {
					errs = append(errs, fmt.Errorf("product %d: %s", r.ID, r.Error.Message))
				}
			}
			g = g[n:]
		}
	}
	return errors.Join(errs...)
}

// do uses ?rest_route= so it works whether or not the site has pretty permalinks.
func (w *Woo) do(method, route string, q url.Values, body, out any) error {
	if q == nil {
		q = url.Values{}
	}
	q.Set("rest_route", "/wc/v3/"+route)
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, w.Base+"/?"+q.Encode(), rd)
	if err != nil {
		return err
	}
	req.SetBasicAuth(w.User, w.Pass)
	req.Header.Set("Content-Type", "application/json")
	resp, err := w.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 500))
		return fmt.Errorf("%s %s: %s %s", method, route, resp.Status, msg)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
