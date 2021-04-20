package api

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/PuerkitoBio/goquery"
)

type Item struct {
	ID            string    `json:"id"`
	Link          string    `json:"link"`
	Title         string    `json:"title"`
	Price         float64   `json:"price"`
	PreviousPrice float64   `json:"previous_price"`
	CreatedAt     time.Time `json:"created_at"`
}

type Client struct {
	client *http.Client
	ctx    context.Context
}

func New(ctx context.Context) (*Client, error) {
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
	if item == nil {
		return fmt.Errorf("api: item is nil")
	}
	u := fmt.Sprintf("https://www.amazon.es/dp/%s", id)
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

	// search price
	sel := doc.Find("#priceblock_ourprice").First()
	if sel == nil {
		return nil
	}
	text := strings.Replace(sel.Text(), ",", ".", 1)
	i := strings.Index(text, ".")
	if i < 0 {
		html, _ := doc.Html()
		return fmt.Errorf("api: price point not found: %s %s %s", id, text, html)
	}
	text = fmt.Sprintf("%s.%s", text[0:i], text[i+1:i+3])
	price, err := strconv.ParseFloat(text, 32)
	if err != nil {
		return fmt.Errorf("api: invalid price value: %s: %w", text, err)
	}

	// search title
	sel = doc.Find("#productTitle").First()
	if sel == nil {
		return nil
	}
	title := strings.Trim(sel.Text(), "\n")

	// search link
	var link string
	doc.Find("link").Each(func(i int, s *goquery.Selection) {
		rel, _ := s.Attr("rel")
		if rel != "canonical" {
			return
		}
		link, _ = s.Attr("href")
	})
	if link == "" {
		return fmt.Errorf("api: link not found: %s", id)
	}

	item.ID = id
	item.Link = link
	item.Title = title
	if item.ID == "" {
		item.Price = price
		item.PreviousPrice = -1
		item.CreatedAt = time.Now().UTC()
	}
	if item.Price < price {
		item.PreviousPrice = item.Price
		item.Price = price
		if err := callback(*item); err != nil {
			return err
		}
	}
	return nil
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
