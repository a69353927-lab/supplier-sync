package main

import (
	"encoding/csv"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// Item is one supplier row after column mapping.
type Item struct {
	SKU, EAN string
	Cost     float64 // 0 = feed has no price, keep store price
	Stock    *int    // nil = feed has no stock, keep store stock
}

// loadFeed reads a CSV or XML feed from a file or URL.
// m maps our field names (sku, ean, cost, stock) to the supplier's column/tag names.
func loadFeed(src string, m map[string]string, xmlItem string, delim rune) (items []Item, skipped int, err error) {
	r, err := open(src)
	if err != nil {
		return nil, 0, err
	}
	defer r.Close()

	var rows []map[string]string
	if xmlItem != "" {
		rows, err = parseXML(r, xmlItem)
	} else {
		rows, err = parseCSV(r, delim)
	}
	if err != nil {
		return nil, 0, err
	}
	items, skipped = toItems(rows, m)
	if len(rows) > 0 && len(items) == 0 {
		return nil, 0, fmt.Errorf("no row has a SKU or EAN; check -map (columns found: %v)", keys(rows[0]))
	}
	return items, skipped, nil
}

func open(src string) (io.ReadCloser, error) {
	if !strings.HasPrefix(src, "http://") && !strings.HasPrefix(src, "https://") {
		return os.Open(src)
	}
	resp, err := (&http.Client{Timeout: 2 * time.Minute}).Get(src)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("feed %s: %s", src, resp.Status)
	}
	return resp.Body, nil
}

func parseCSV(r io.Reader, delim rune) ([]map[string]string, error) {
	cr := csv.NewReader(r)
	cr.Comma = delim
	cr.FieldsPerRecord = -1
	cr.LazyQuotes = true
	recs, err := cr.ReadAll()
	if err != nil || len(recs) == 0 {
		return nil, err
	}
	head := recs[0]
	head[0] = strings.TrimPrefix(head[0], "\uFEFF") // Excel BOM
	rows := make([]map[string]string, 0, len(recs)-1)
	for _, rec := range recs[1:] {
		row := map[string]string{}
		for i, v := range rec {
			if i < len(head) {
				row[strings.TrimSpace(head[i])] = v
			}
		}
		rows = append(rows, row)
	}
	return rows, nil
}

// parseXML turns every <itemTag> element into a row of child-tag → text.
// Attributes of itemTag are available as "@name".
func parseXML(r io.Reader, itemTag string) ([]map[string]string, error) {
	d := xml.NewDecoder(r)
	d.CharsetReader = latin1
	var (
		rows  []map[string]string
		cur   map[string]string
		field string
		text  strings.Builder
	)
	for {
		tok, err := d.Token()
		if err == io.EOF {
			return rows, nil
		}
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if t.Name.Local == itemTag {
				cur = map[string]string{}
				for _, a := range t.Attr {
					cur["@"+a.Name.Local] = a.Value
				}
			} else if cur != nil {
				field = t.Name.Local
				text.Reset()
			}
		case xml.CharData:
			if field != "" {
				text.Write(t)
			}
		case xml.EndElement:
			if t.Name.Local == itemTag && cur != nil {
				rows = append(rows, cur)
				cur, field = nil, ""
			} else if t.Name.Local == field {
				cur[field] = strings.TrimSpace(text.String())
				field = ""
			}
		}
	}
}

// ponytail: cp1252 is decoded as latin1 (differs only in 0x80–0x9F); add golang.org/x/text if a supplier needs more.
func latin1(charset string, in io.Reader) (io.Reader, error) {
	switch strings.ToLower(charset) {
	case "iso-8859-1", "latin1", "windows-1252":
	default:
		return nil, fmt.Errorf("unsupported feed charset %q", charset)
	}
	b, err := io.ReadAll(in)
	if err != nil {
		return nil, err
	}
	rs := make([]rune, len(b))
	for i, c := range b {
		rs[i] = rune(c)
	}
	return strings.NewReader(string(rs)), nil
}

func toItems(rows []map[string]string, m map[string]string) (items []Item, skipped int) {
	for _, row := range rows {
		it := Item{SKU: strings.TrimSpace(row[m["sku"]]), EAN: strings.TrimSpace(row[m["ean"]])}
		if it.SKU == "" && it.EAN == "" {
			skipped++
			continue
		}
		it.Cost, _ = num(row[m["cost"]])
		if v, ok := num(row[m["stock"]]); ok {
			n := max(int(v), 0)
			it.Stock = &n
		}
		items = append(items, it)
	}
	return items, skipped
}

// num parses supplier numbers: "12.50", "12,50", "€1.234,50", "1,234.50".
// The last separator is taken as the decimal point.
func num(s string) (float64, bool) {
	s = strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' || r == ',' || r == '.' || r == '-' {
			return r
		}
		return -1
	}, s)
	if s == "" {
		return 0, false
	}
	if strings.LastIndex(s, ",") > strings.LastIndex(s, ".") {
		s = strings.ReplaceAll(s, ".", "")
		s = strings.Replace(s, ",", ".", 1)
	} else {
		s = strings.ReplaceAll(s, ",", "")
	}
	v, err := strconv.ParseFloat(s, 64)
	return v, err == nil
}

func keys(m map[string]string) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	return ks
}
