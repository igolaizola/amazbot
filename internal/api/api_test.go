package api

import (
	"bytes"
	_ "embed"
	"fmt"
	"testing"

	"github.com/PuerkitoBio/goquery"
)

var (
	//go:embed html/de.html
	de []byte
	//go:embed html/es.html
	es []byte
	//go:embed html/co.uk.html
	couk []byte
	//go:embed html/co.jp.html
	cojp []byte
	//go:embed html/com.br.html
	combr []byte
	//go:embed html/com.au.html
	comau []byte
	//go:embed html/ca.html
	ca []byte
	//go:embed html/com.html
	com []byte
)

func TestPrices(t *testing.T) {
	tests := map[string]struct {
		html []byte
		want string
	}{
		"es":     {es, "11.49 11.50 10.22 10.22 0.00"},
		"de":     {de, "10.99 10.21 10.22 10.22 0.00"},
		"co.uk":  {couk, "15.27 0.00 0.00 0.00 0.00"},
		"co.jp":  {cojp, "3900.00 0.00 0.00 0.00 0.00"},
		"com.br": {combr, "164.00 0.00 0.00 0.00 0.00"},
		"com.au": {comau, "37.98 0.00 0.00 0.00 0.00"},
		"ca":     {ca, "29.83 0.00 0.00 0.00 0.00"},
		"com":    {com, "18.04 0.00 0.00 0.00 0.00"},
	}
	for domain, tt := range tests {
		tt := tt
		t.Run(domain, func(t *testing.T) {
			doc, err := goquery.NewDocumentFromReader(bytes.NewReader(tt.html))
			if err != nil {
				t.Fatal(err)
			}
			var p [5]float64
			p = extractPrices(domain, "", doc, p)
			got := fmt.Sprintf("%.2f %.2f %.2f %.2f %.2f", p[0], p[1], p[2], p[2], p[4])
			if tt.want != got {
				t.Errorf("invalid price: want %s, got %s", tt.want, got)
			}
		})
	}
}
