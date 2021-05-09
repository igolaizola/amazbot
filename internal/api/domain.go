package api

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

func usedText(domain string) string {
	switch domain {
	case "es":
		return "De 2ª mano"
	case "de":
		return "Gebraucht"
	case "fr":
		return "D'occasion"
	case "it":
		return "Usato"
	default:
		return "Used"
	}
}

func statesText(domain string) [5]string {
	switch domain {
	case "es":
		return [5]string{"Nuevo", "Como nuevo", "Muy bueno", "Bueno", "Aceptable"}
	case "de":
		return [5]string{"Neu", "Wie neu", "Sehr gut", "Gut", "Akzeptabel"}
	case "fr":
		return [5]string{"Neuf", "Comme neuf", "Très bon", "Bon", "Acceptable"}
	case "it":
		return [5]string{"Nuovo", "Come nuovo", "Ottime condizioni", "Buone condizioni", "Condizioni accettabili"}
	case "com.br":
		return [5]string{"Novo", "Como novo", "Muito bom", "Bom", "Aceitável"}
	default:
		return [5]string{"New", "Like new", "Very good", "Good", "Acceptable"}
	}
}

func StateText(domain string, s int) string {
	if s < 0 || s >= 5 {
		return ""
	}
	states := statesText(domain)
	return states[s]
}

func Coin(domain string) string {
	switch domain {
	case "com", "ca", "com.au":
		return "$"
	case "co.uk":
		return "£"
	case "co.jp":
		return "¥"
	case "com.br":
		return "R$"
	default:
		return "€"
	}
}

var priceRegex = map[string]*regexp.Regexp{
	"es":     regexp.MustCompile(`([.0-9]+),([0-9][0-9]) €`),
	"it":     regexp.MustCompile(`([.0-9]+),([0-9][0-9]) €`),
	"fr":     regexp.MustCompile(`([ 0-9]+),([0-9][0-9]) €`),
	"de":     regexp.MustCompile(`([.0-9]+),([0-9][0-9]) €`),
	"co.uk":  regexp.MustCompile(`£([,0-9]+).([0-9][0-9])`),
	"co.jp":  regexp.MustCompile(`¥([,0-9]+)`),
	"ca":     regexp.MustCompile(`\$([,0-9]+).([0-9][0-9])`),
	"com.au": regexp.MustCompile(`\$([,0-9]+).([0-9][0-9])`),
	"com":    regexp.MustCompile(`\$([,0-9]+).([0-9][0-9])`),
	"com.br": regexp.MustCompile(`R\$([.0-9]+),([0-9][0-9])`),
}

func parsePrice(domain, text string) (float64, error) {
	text = strings.Replace(text, string('\u00A0'), " ", -1)
	re, ok := priceRegex[domain]
	if !ok {
		return 0, fmt.Errorf("api: invalid domain: %s", domain)
	}
	sm := re.FindStringSubmatch(text)
	if len(sm) < 2 {
		return 0, errors.New("api: price not found")
	}
	v := strings.Replace(sm[1], ".", "", -1)
	v = strings.Replace(v, ",", "", -1)
	v = strings.Replace(v, " ", "", -1)
	dec := "00"
	if len(sm) > 2 {
		dec = sm[2]
	}
	price, err := strconv.ParseFloat(fmt.Sprintf("%s.%s", v, dec), 32)
	if err != nil {
		return 0, err
	}
	return price, nil
}
