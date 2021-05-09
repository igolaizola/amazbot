package api

import (
	"bytes"
	_ "embed"
	"fmt"
	"testing"

	"github.com/PuerkitoBio/goquery"
)

//go:embed html/de.html
var de []byte

//go:embed html/es.html
var es []byte

//go:embed html/co.uk.html
var couk []byte

func TestPrices(t *testing.T) {
	tests := map[string]struct {
		html []byte
		want string
	}{
		"es":    {es, "11.50"},
		"de":    {de, "10.21"},
		"co.uk": {couk, "10.21"},
	}
	for domain, tt := range tests {
		tt := tt
		t.Run(domain, func(t *testing.T) {
			doc, err := goquery.NewDocumentFromReader(bytes.NewReader(tt.html))
			if err != nil {
				t.Fatal(err)
			}
			var prices [5]float64
			prices = extractPrices(domain, "", doc, prices)
			if fmt.Sprintf("%.2f", prices[0]) != tt.want {
				t.Errorf("invalid price: want %f, got %f", 10.21, prices[0])
			}
		})
	}
}
