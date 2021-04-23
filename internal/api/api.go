package api

import (
	"context"
	"errors"
	"fmt"
	"io/ioutil"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/PuerkitoBio/goquery"
)

type Item struct {
	ID                string    `json:"id"`
	Link              string    `json:"link"`
	Title             string    `json:"title"`
	Price             float64   `json:"price"`
	PreviousPrice     float64   `json:"previous_price"`
	UsedPrice         float64   `json:"used_price"`
	PreviousUsedPrice float64   `json:"previous_used_price"`
	CreatedAt         time.Time `json:"created_at"`
}

type Client struct {
	client *http.Client
	ctx    context.Context
	python string
}

func New(ctx context.Context, python string) (*Client, error) {
	cookieJar, err := cookiejar.New(nil)
	if err != nil {
		return nil, fmt.Errorf("api: could not create cookie jar: %w", err)
	}
	cli := &Client{
		ctx: ctx,
		client: &http.Client{
			Timeout: 10 * time.Second,
			Transport: &transport{
				ctx: ctx,
			},
			Jar: cookieJar,
		},
		python: python,
	}
	cli.client.Get("https://www.amazon.es")
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

func (c *Client) Search(id string, item *Item, callback func(Item) error) error {
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
		if errors.Is(err, errBadGateway) {
			continue
		}
		return err
	}
}

var errBadGateway = errors.New("api: 502 bad gateway")

func (c *Client) search(id string, item *Item, callback func(Item) error) error {
	u := fmt.Sprintf("https://www.amazon.es/dp/%s", id)
	return c.searchURL(u, id, item, callback)
}

func (c *Client) searchURL(u string, id string, item *Item, callback func(Item) error) error {
	if item == nil {
		return fmt.Errorf("api: item is nil")
	}
	r, err := c.client.Get(u)
	if err != nil {
		return fmt.Errorf("api: get request failed: %w", err)
	}
	if r.StatusCode == 502 {
		return errBadGateway
	}
	if r.StatusCode != 200 {
		return fmt.Errorf("api: invalid status code: %s", r.Status)
	}
	defer r.Body.Close()

	doc, err := goquery.NewDocumentFromReader(r.Body)
	if err != nil {
		return err
	}

	// search captcha
	captcha := false
	doc.Find("#captchacharacters").EachWithBreak(func(i int, s *goquery.Selection) bool {
		captcha = true
		return false
	})
	if captcha {
		var img string
		doc.Find("form img").EachWithBreak(func(i int, s *goquery.Selection) bool {
			if v, ok := s.Attr("src"); ok {
				img = v
				return false
			}
			return true
		})
		if img == "" {
			return fmt.Errorf("api: couldn't get captcha image: %s", id)
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
			return fmt.Errorf("api: couldn't get amzn value: %s", id)
		}
		if amznr == "" {
			return fmt.Errorf("api: couldn't get amzn-r value: %s", id)
		}

		// resolve captcha
		out, err := exec.Command(c.python, "captcha.py", img).Output()
		if err != nil {
			return fmt.Errorf("api: captcha command failed: %s %w", out, err)
		}
		solution := strings.TrimSpace(string(out))
		if solution == "" {
			return fmt.Errorf("api: solved captcha is empty")
		}

		u, err := url.Parse("https://www.amazon.es/errors/validateCaptcha")
		if err != nil {
			return fmt.Errorf("api: couldn't parse url: %w", err)
		}
		q := u.Query()
		q.Set("amzn", amzn)
		q.Set("amzn-r", amznr)
		q.Set("field-keywords", solution)
		u.RawQuery = q.Encode()
		return c.searchURL(u.String(), id, item, callback)
	}

	// search title
	var title string
	doc.Find("#productTitle").EachWithBreak(func(i int, s *goquery.Selection) bool {
		title = strings.TrimSpace(s.Text())
		return false
	})
	if title == "" {
		h, _ := doc.Html()
		ioutil.WriteFile("err.html", []byte(h), 0644)
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

	// search price new
	var new string
	doc.Find("#priceblock_ourprice").EachWithBreak(func(i int, s *goquery.Selection) bool {
		new = s.Text()
		return false
	})
	if new == "" {
		return fmt.Errorf("api: price not found: %s", id)
	}
	price, err := parsePrice(new)
	if err != nil {
		return fmt.Errorf("api: couldn't parse new price: %s: %w", id, err)
	}

	// search price used
	var used string
	doc.Find("#olpLinkWidget_feature_div span.a-size-base.a-color-base").EachWithBreak(func(i int, s *goquery.Selection) bool {
		used = s.Text()
		return false
	})
	var usedPrice float64
	if used != "" {
		usedPrice, err = parsePrice(used)
		if err != nil {
			return fmt.Errorf("api: couldn't parse used price: %s: %w", id, err)
		}
	}

	item.ID = id
	item.Link = link
	item.Title = title
	if item.ID == "" {
		item.Price = price
		item.PreviousUsedPrice = 0
		item.UsedPrice = usedPrice
		item.PreviousPrice = 0
		item.CreatedAt = time.Now().UTC()
	}
	item.PreviousUsedPrice = item.UsedPrice
	item.UsedPrice = usedPrice

	if item.Price < price {
		item.PreviousPrice = item.Price
		item.Price = price
		if err := callback(*item); err != nil {
			return err
		}
	}
	if item.UsedPrice > 0 && item.UsedPrice < item.Price {
		if item.PreviousUsedPrice <= 0 || item.UsedPrice < item.PreviousUsedPrice {
			if err := callback(*item); err != nil {
				return err
			}
		}
	}
	return nil
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

type transport struct {
	lock sync.Mutex
	ctx  context.Context
}

func (t *transport) RoundTrip(r *http.Request) (*http.Response, error) {
	r.Header.Set("cache-control", "max-age=0")
	r.Header.Set("rtt", "150")
	r.Header.Set("downlink", "10")
	r.Header.Set("ect", "4g")
	r.Header.Set("sec-ch-ua", `"Google Chrome";v="89", "Chromium";v="89", ";Not A Brand";v="99"`)
	r.Header.Set("sec-ch-ua-mobile", "?0")
	r.Header.Set("upgrade-insecure-requests", "1")
	r.Header.Set("user-agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/89.0.4389.128 Safari/537.36")
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
	return http.DefaultTransport.RoundTrip(r)
}
