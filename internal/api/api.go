package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/PuerkitoBio/goquery"
	"golang.org/x/net/proxy"
)

type Item struct {
	ID       string     `json:"id"`
	Link     string     `json:"link"`
	Title    string     `json:"title"`
	MinPrice float64    `json:"min_price"`
	Prices   [5]float64 `json:"prices"`
}

type Client struct {
	client     *http.Client
	ctx        context.Context
	captchaURL string
	transport  *transport
}

func New(ctx context.Context, captchaURL, proxyURL string) (*Client, error) {
	captchaURL = strings.TrimLeft(captchaURL, "/")
	if captchaURL != "" {
		_, err := url.Parse(captchaURL)
		if err != nil {
			return nil, fmt.Errorf("api: couldn't parse captcha service url %s: %w", captchaURL, err)
		}
	}
	tr, err := newTransport(ctx, proxyURL)
	if err != nil {
		return nil, err
	}
	cli := &Client{
		ctx: ctx,
		client: &http.Client{
			Timeout:   30 * time.Second,
			Transport: tr,
		},
		captchaURL: captchaURL,
		transport:  tr,
	}
	if err := cli.reset(); err != nil {
		return nil, err
	}
	// test captcha resolver
	if captchaURL != "" {
		c, err := cli.resolveCaptcha("https://images-na.ssl-images-amazon.com/captcha/usvmgloq/Captcha_kwrrnqwkph.jpg")
		switch {
		case err != nil:
			log.Println(err)
		case c != "AAFXMX":
			log.Println(fmt.Errorf("api: captcha resolver failed: %s", c))
		default:
			log.Println("api: captcha resolver test succeeded")
		}
	}
	return cli, nil
}

func ItemID(link string) (string, bool) {
	// Isolate link
	idx := strings.Index(link, "http")
	if idx < 0 {
		return "", false
	}
	link = link[idx:]
	link = strings.Split(link, " ")[0]

	// Parse url and get product id
	u, err := url.Parse(link)
	if err != nil {
		return "", false
	}
	if !strings.Contains(u.Host, "amazon.es") {
		return "", false
	}
	split := strings.Split(u.Path, "/")
	var prev string
	for _, s := range split {
		if prev == "dp" {
			return s, true
		}
		prev = s
	}
	return "", false
}

func StateText(s int) string {
	switch s {
	case 0:
		return "Nuevo"
	case 1:
		return "Como nuevo"
	case 2:
		return "Muy bueno"
	case 3:
		return "Bueno"
	case 4:
		return "Aceptable"
	}
	return ""
}

func (c *Client) Search(id string, item *Item, callback func(Item, int) error) error {
	for {
		select {
		case <-c.ctx.Done():
			return nil
		default:
		}
		err := c.search(id, item, callback)
		var netErr net.Error
		if errors.As(err, &netErr) && netErr.Timeout() {
			continue
		}
		if errors.Is(err, errRetry) {
			continue
		}
		return err
	}
}

var errRetry = errors.New("api: retriable error")

func (c *Client) search(id string, item *Item, callback func(Item, int) error) error {
	u := fmt.Sprintf("https://www.amazon.es/dp/%s", id)
	return c.searchURL(u, id, item, callback)
}

func (c *Client) searchURL(u string, id string, item *Item, callback func(Item, int) error) error {
	if item == nil {
		return fmt.Errorf("api: item is nil")
	}
	doc, err := c.getDoc(u, id, 0)
	if err != nil {
		return err
	}

	// search title
	var title string
	doc.Find("#productTitle").EachWithBreak(func(i int, s *goquery.Selection) bool {
		title = strings.TrimSpace(s.Text())
		return false
	})
	if title == "" {
		h, _ := doc.Html()
		ioutil.WriteFile(fmt.Sprintf("%s_err.html", id), []byte(h), 0644)
		return fmt.Errorf("api: title not found: %s", id)
	}

	// search link
	var link string
	doc.Find("link").EachWithBreak(func(i int, s *goquery.Selection) bool {
		rel, _ := s.Attr("rel")
		if rel != "canonical" {
			return true
		}
		link, _ = s.Attr("href")
		return false
	})
	if link == "" {
		return fmt.Errorf("api: link not found: %s", id)
	}

	var prices [5]float64
	var sha [32]byte
	i := 0
	for {
		u = fmt.Sprintf("https://www.amazon.es/gp/aod/ajax/ref=aod_page_2?asin=%s&pc=dp&pageno=%d", id, i)
		doc, err := c.getDoc(u, id, 0)
		if err != nil {
			return err
		}
		currSHA := sha256.Sum256([]byte(doc.Text()))
		if bytes.Equal(sha[:], currSHA[:]) {
			break
		}
		sha = currSHA
		if i > 10 {
			break
		}
		i++

		divs := [][2]string{
			// First pinned offer
			{"#pinned-de-id", "#pinned-offer-top-id"},
			// Other offers
			{"#aod-offer", "#aod-offer-price"},
		}
		for _, div := range divs {
			doc.Find(div[0]).Each(func(i int, s *goquery.Selection) {
				state := -1
				s.Find(fmt.Sprintf("%s #aod-offer-heading", div[0])).EachWithBreak(func(i int, s *goquery.Selection) bool {
					text := s.Text()
					text = strings.TrimSpace(text)
					text = strings.Replace(text, "De 2ª mano", "", 1)
					text = strings.Replace(text, "-", "", 1)
					text = strings.TrimSpace(text)
					switch text {
					case "Nuevo":
						state = 0
					case "Como nuevo":
						state = 1
				  case "Muy bueno":
					  state = 2
					case "Bueno":
					 state = 3
					case "Aceptable":
					 state = 4
					}
					return false
				})
				if state < 0 {
					return
				}
				var delivery float64
				s.Find(fmt.Sprintf("%s %s #ddmDeliveryMessage", div[0], div[1])).EachWithBreak(func(i int, s *goquery.Selection) bool {
					text := s.Text()
					text = strings.TrimSpace(text)
					if !strings.HasPrefix(text, "Por ") {
						return true
					}
					idx := strings.Index(text, "€")
					if idx < 4 {
						return true
					}
					text = text[4:idx]
					price, err := parsePrice(text)
					if err != nil {
						log.Println(fmt.Errorf("api: couldn't parse delivery price %s %s: %w", text, id, err))
						return true
					}
					delivery = price
					return false
				})
				s.Find(fmt.Sprintf("%s %s .a-offscreen", div[0], div[1])).EachWithBreak(func(i int, s *goquery.Selection) bool {
					text := s.Text()
					price, err := parsePrice(text)
					if err != nil {
						log.Println(fmt.Errorf("api: couldn't parse price %s %s: %w", text, id, err))
						return true
					}
					price = price + delivery
					if prices[state] == 0 || price < prices[state] {
						prices[state] = price
					}
					return false
				})
			})
		}

	}

	found := false
	for _, p := range prices {
		if p == 0 {
			continue
		}
		found = true
		break
	}

	if !found {
		h, _ := doc.Html()
		ioutil.WriteFile(fmt.Sprintf("%s_err.html", id), []byte(h), 0644)
		return fmt.Errorf("api: prices not found: %s", id)
	}

	log.Println("prices", prices)

	item.ID = id
	item.Link = link
	item.Title = title
	prevMin := item.MinPrice
	if item.MinPrice == 0 || prices[0] < item.MinPrice {
		item.MinPrice = prices[0]
	}
	prev := item.Prices
	for i, p := range prices {
		item.Prices[i] = p
	}
	item.Prices = prices
	for i, p := range prices {
	  // TODO(igolaizola): disabled some states
	  if i > 1 {
	    break
	  }
		// Price not found, continue
		if p == 0 {
			continue
		}
		// Skip first stored min price
		if prevMin == 0 && i == 0 {
			continue
		}
		// Skip prices higher than previous ones
		if prev[i] > 0 && p >= prev[i] {
			continue
		}
		// Skip prices higher than min
		if item.MinPrice > 0 && p >= item.MinPrice {
			continue
		}
		if err := callback(*item, i); err != nil {
			return err
		}
	}

	return nil
}

func (c *Client) getDoc(u string, id string, depth int) (*goquery.Document, error) {
	req, err := http.NewRequest("GET", u, nil)
	if err != nil {
		return nil, fmt.Errorf("api: couldn't create request: %w", err)
	}
	return c.getDocWithReq(req, id, depth)
}

func (c *Client) getDocWithReq(req *http.Request, id string, depth int) (*goquery.Document, error) {
	if depth > 2 {
		return nil, fmt.Errorf("api: recursion aborted on depth %d", depth)
	}
	log.Printf("request %s: %s\n", req.URL, id)
	r, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("api: get request failed: %w", err)
	}
	if r.StatusCode == 502 {
		return nil, errRetry
	}
	if r.StatusCode == 503 {
		c.reset()
	}
	if r.StatusCode != 200 && r.StatusCode != 202 {
		return nil, fmt.Errorf("api: invalid status code: %s", r.Status)
	}
	defer r.Body.Close()

	doc, err := goquery.NewDocumentFromReader(r.Body)
	if err != nil {
		return nil, err
	}

	// search captcha
	captcha := false
	doc.Find("#captchacharacters").EachWithBreak(func(i int, s *goquery.Selection) bool {
		captcha = true
		return false
	})
	if captcha {
		log.Printf("captcha requested: %s", id)
		var img string
		doc.Find("form img").EachWithBreak(func(i int, s *goquery.Selection) bool {
			if v, ok := s.Attr("src"); ok {
				img = v
				return false
			}
			return true
		})
		if img == "" {
			return nil, fmt.Errorf("api: couldn't get captcha image: %s", id)
		}
		var amzn string
		var amznr string
		doc.Find("form input").Each(func(i int, s *goquery.Selection) {
			val, ok := s.Attr("value")
			if !ok {
				return
			}
			name, ok := s.Attr("name")
			if !ok {
				return
			}
			switch name {
			case "amzn":
				amzn = val
			case "amzn-r":
				amznr = val
			}
		})
		if amzn == "" {
			return nil, fmt.Errorf("api: couldn't get amzn value: %s", id)
		}
		if amznr == "" {
			return nil, fmt.Errorf("api: couldn't get amzn-r value: %s", id)
		}

		// resolve captcha
		solution, err := c.resolveCaptcha(img)
		if err != nil {
			return nil, err
		}

		u, err := url.Parse("https://www.amazon.es/errors/validateCaptcha")
		if err != nil {
			return nil, fmt.Errorf("api: couldn't parse url: %w", err)
		}
		q := u.Query()
		q.Set("amzn", amzn)
		q.Set("amzn-r", amznr)
		q.Set("field-keywords", solution)
		u.RawQuery = q.Encode()
		return c.getDoc(u.String(), id, depth+1)
	}
	return doc, nil
}

func (c *Client) resolveCaptcha(link string) (string, error) {
	if c.captchaURL == "" {
		return "", errors.New("api:missing captcha service")
	}
	u := fmt.Sprintf("%s/%s", c.captchaURL, link)
	client := &http.Client{
		Timeout: 10 * time.Second,
	}
	r, err := client.Get(u)
	if err != nil {
		return "", fmt.Errorf("api: get request failed: %w", err)
	}
	if r.StatusCode != 200 {
		return "", fmt.Errorf("api: invalid status code: %s", r.Status)
	}
	defer r.Body.Close()
	body, err := ioutil.ReadAll(r.Body)
	if err != nil {
		return "", fmt.Errorf("api: error reading body: %w", err)
	}
	captcha := string(body)
	if captcha == "" {
		return "", fmt.Errorf("api: resolved captcha is empty")
	}
	return captcha, nil
}

func (c *Client) reset() error {
	c.transport.userAgent = randomUserAgent()
	cookieJar, err := cookiejar.New(nil)
	if err != nil {
		return fmt.Errorf("api: could not create cookie jar: %w", err)
	}
	c.client.Jar = cookieJar
	u := "https://www.amazon.es"
	doc, err := c.getDoc(u, "", 0)
	if err != nil {
		return err
	}
	postalCode := "44001"
	hasLocation := false
	doc.Find("#glow-ingress-line2").EachWithBreak(func(i int, s *goquery.Selection) bool {
		if !strings.Contains(s.Text(), postalCode) {
			return true
		}
		hasLocation = true
		return false
	})
	if !hasLocation {
		if err := c.changeLocation(doc, postalCode); err != nil {
			return err
		}
	}

	return nil
}

func (c *Client) changeLocation(doc *goquery.Document, postalCode string) error {
	modal := locationModal{}
	doc.Find("#nav-global-location-data-modal-action").EachWithBreak(func(i int, s *goquery.Selection) bool {
		data, ok := s.Attr("data-a-modal")
		if !ok {
			return true
		}
		if err := json.Unmarshal([]byte(data), &modal); err != nil {
			log.Println(fmt.Errorf("api: couldn't unmarshal location modal: %w", err))
			return true
		}
		return false
	})
	if modal.URL == "" {
		return fmt.Errorf("api: couldn't find location modal")
	}

	u := fmt.Sprintf("https://www.amazon.es/%s", strings.TrimLeft(modal.URL, "/"))
	req, err := http.NewRequest("GET", u, nil)
	if err != nil {
		return fmt.Errorf("api: couldn't create post request: %w", err)
	}
	req.Header.Add("anti-csrftoken-a2z", modal.Ajax.Token)
	doc, err = c.getDocWithReq(req, "", 0)
	if err != nil {
		return err
	}

	var token string
	doc.Find("script").EachWithBreak(func(i int, s *goquery.Selection) bool {
		text := s.Text()
		idx := strings.Index(text, "CSRF_TOKEN")
		if idx < 0 {
			return true
		}
		split := strings.Split(text[idx:], "\"")
		if len(split) < 2 {
			return true
		}
		token = split[1]
		if token == "" {
			return false
		}
		return true
	})

	u = "https://www.amazon.es/gp/delivery/ajax/address-change.html"
	form := url.Values{}
	form.Add("locationType", "LOCATION_INPUT")
	form.Add("zipCode", postalCode)
	form.Add("storeContext", "generic")
	form.Add("deviceType", "web")
	form.Add("pageType", "Gateway")
	form.Add("actionSource", "glow")
	form.Add("almBrandId", "undefined")
	req, err = http.NewRequest("POST", u, strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("api: couldn't create post request: %w", err)
	}
	req.Header.Add("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Add("anti-csrftoken-a2z", token)
	_, err = c.getDocWithReq(req, "", 0)
	if err != nil {
		return fmt.Errorf("api: post request failed: %w", err)
	}
	return nil
}

type locationModal struct {
	Ajax ajaxHeaders `json:"ajaxHeaders"`
	URL  string      `json:"url"`
}

type ajaxHeaders struct {
	Token string `json:"anti-csrftoken-a2z"`
}

func parsePrice(text string) (float64, error) {
	text = strings.TrimSpace(text)
	text = strings.Trim(text, "€$")
	text = strings.TrimSpace(text)
	text = strings.Replace(text, ".", "", -1)
	text = strings.Replace(text, ",", ".", 1)
	price, err := strconv.ParseFloat(text, 32)
	if err != nil {
		return 0, err
	}
	return price, nil
}

func newTransport(ctx context.Context, proxyURL string) (*transport, error) {
	tr := http.DefaultTransport
	if proxyURL != "" {
		u, err := url.Parse(proxyURL)
		if err != nil {
			return nil, fmt.Errorf("api: couldn't parse proxy %s: %w", proxyURL, err)
		}
		switch u.Scheme {
		case "socks5":
			// Create a socks5 dialer
			dialer, err := proxy.SOCKS5("tcp", u.Host, nil, proxy.Direct)
			if err != nil {
				return nil, fmt.Errorf("api: couldn't create socks5 proxy: %w", err)
			}
			tr = &http.Transport{
				Dial: dialer.Dial,
			}
		default:
			tr = &http.Transport{Proxy: http.ProxyURL(u)}
		}
		if u.Scheme != "socks5" {
			return nil, fmt.Errorf("api: unsupported scheme: %s", u.Scheme)
		}
	}
	return &transport{
		ctx: ctx,
		tr:  tr,
	}, nil
}

type transport struct {
	lock      sync.Mutex
	ctx       context.Context
	tr        http.RoundTripper
	userAgent string
}

func (t *transport) RoundTrip(r *http.Request) (*http.Response, error) {
	r.Header.Set("cache-control", "max-age=0")
	r.Header.Set("rtt", "150")
	r.Header.Set("downlink", "10")
	r.Header.Set("ect", "4g")
	r.Header.Set("sec-ch-ua", `"Google Chrome";v="89", "Chromium";v="89", ";Not A Brand";v="99"`)
	r.Header.Set("sec-ch-ua-mobile", "?0")
	r.Header.Set("upgrade-insecure-requests", "1")
	r.Header.Set("user-agent", t.userAgent)
	r.Header.Set("accept", "ext/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8,application/signed-exchange;v=b3;q=0.9")
	r.Header.Set("sec-fetch-site", "none")
	r.Header.Set("sec-fetch-mode", "navigate")
	r.Header.Set("sec-fetch-user", "?1")
	r.Header.Set("sec-fetch-dest", "document")
	r.Header.Set("accept-language", "es-ES,es;q=0.9,en-US;q=0.8,en;q=0.7,eu;q=0.6,fr;q=0.5")

	t.lock.Lock()
	defer func() {
		select {
		case <-t.ctx.Done():
		case <-time.After(5000 * time.Millisecond):
		}
		t.lock.Unlock()
	}()
	return t.tr.RoundTrip(r)
}
