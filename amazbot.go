package amazbot

import (
	"context"
	"fmt"
	"io/ioutil"
	"log"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	tgbot "github.com/go-telegram-bot-api/telegram-bot-api"
	"github.com/igolaizola/amazbot/internal/api"
	"github.com/igolaizola/amazbot/internal/store"
)

type bot struct {
	*tgbot.BotAPI
	db      *store.Store
	searchs sync.Map
	dups    sync.Map
	admin   int
	client  *api.Client
	wg      sync.WaitGroup
	elapsed time.Duration
}

var captcha = `from amazoncaptcha import AmazonCaptcha
import sys

link = sys.argv[1]
captcha = AmazonCaptcha.fromlink(link)
solution = captcha.solve()
print(solution)
`

func Run(ctx context.Context, python, token, dbPath string, admin int, users []int) error {
	err := ioutil.WriteFile("captcha.py", []byte(captcha), 0644)

	db, err := store.New(dbPath)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	botAPI, err := tgbot.NewBotAPI(token)
	if err != nil {
		return fmt.Errorf("couldn't create bot api: %w", err)
	}
	//botAPI.Debug = true

	apiCli, err := api.New(ctx, python)
	if err != nil {
		return fmt.Errorf("couldn't create api client: %w", err)
	}
	bot := &bot{
		BotAPI: botAPI,
		db:     db,
		client: apiCli,
		admin:  admin,
	}

	users = append(users, admin)
	userChats := make(map[int]string)
	for _, u := range users {
		userChats[u] = strconv.Itoa(u)
		var chat string
		if err := db.Get("config", strconv.Itoa(u), &chat); err != nil {
			bot.log(fmt.Errorf("couldn't get config for %d: %w", u, err))
			continue
		}
		if chat != "" {
			userChats[u] = chat
		}
	}

	bot.log(fmt.Sprintf("amazbot started, bot %s", bot.Self.UserName))
	defer bot.log(fmt.Sprintf("amazbot stoped, bot %s", bot.Self.UserName))
	defer bot.wg.Wait()

	keys, err := db.Keys("db")
	if err != nil {
		bot.log(fmt.Errorf("couldn't get keys: %w", err))
	}
	for _, k := range keys {
		if _, err := parseArgs(k, ""); err != nil {
			bot.log(fmt.Errorf("couldn't parse key %s: %w", k, err))
			continue
		}
		bot.searchs.Store(k, nil)
		bot.log(fmt.Sprintf("loaded from db: %s", k))
	}

	bot.wg.Add(1)
	go func() {
		defer log.Println("search routine finished")
		defer bot.wg.Done()
		for {
			start := time.Now()
			var keys []string
			bot.searchs.Range(func(k interface{}, _ interface{}) bool {
				keys = append(keys, k.(string))
				return true
			})
			sort.Strings(keys)
			for _, k := range keys {
				log.Println(fmt.Sprintf("searching: %s", k))
				select {
				case <-ctx.Done():
					return
				default:
				}
				if _, ok := bot.searchs.Load(k); !ok {
					continue
				}
				parsed, err := parseArgs(k, "")
				if err != nil {
					bot.log(fmt.Errorf("couldn't parse key %s: %w", k, err))
					continue
				}
				bot.search(ctx, parsed)
			}
			bot.elapsed = time.Since(start)

			select {
			case <-ctx.Done():
				return
			case <-time.After(5 * time.Second):
			}
		}
	}()

	u := tgbot.NewUpdate(0)
	u.Timeout = 60
	updates, err := bot.GetUpdatesChan(u)
	if err != nil {
		bot.log(fmt.Errorf("couldn't get update chan: %w", err))
		return err
	}
	for {
		var update tgbot.Update
		select {
		case <-ctx.Done():
			log.Println("stopping bot")
			return nil
		case update = <-updates:
		}
		if update.Message == nil {
			continue
		}

		// Print chat ID when added to a group or channel
		bot.printChatID(update.Message)

		user := int(update.Message.Chat.ID)
		if _, ok := userChats[user]; !ok {
			continue
		}

		// Launch search from link pasted
		if id, ok := api.ItemID(update.Message.Text); ok {
			parsed, err := parseArgs(id, userChats[user])
			if err != nil {
				bot.message(user, err.Error())
			} else {
				bot.searchs.Store(parsed.id, nil)
			}
			bot.message(user, fmt.Sprintf("searching %s", parsed.id))
		}

		if update.Message.IsCommand() {
			args := update.Message.CommandArguments()
			switch update.Message.Command() {
			case "chat":
				if args == "" {
					bot.message(user, fmt.Sprintf("current chat id for searchs: %s", userChats[user]))
					break
				}
				userChats[user] = args
				if err := db.Put("config", strconv.Itoa(user), args); err != nil {
					bot.log(fmt.Errorf("couldn't get config for %d: %w", u, err))
				}
				bot.message(user, fmt.Sprintf("chat id for searchs updated: %s", args))
			case "search":
				if args == "" {
					bot.message(user, "search arguments not provided")
					continue
				}
				parsed, err := parseArgs(args, userChats[user])
				if err != nil {
					bot.message(user, err.Error())
				} else {
					bot.searchs.Store(parsed.id, struct{}{})
				}
				bot.message(user, fmt.Sprintf("searching %s", parsed.id))
			case "status":
				all := false
				if args == "*" {
					all = true
				}
				bot.message(user, "status info:")
				bot.searchs.Range(func(k interface{}, v interface{}) bool {
					key := k.(string)
					if !all {
						prefix := fmt.Sprintf("%s/", userChats[user])
						if !strings.HasPrefix(key, prefix) {
							return true
						}
						key = strings.TrimPrefix(key, prefix)
					}
					var link string
					var price float64
					var usedPrice string
					if i, ok := v.(api.Item); ok {
						link = i.Link
						price = i.Price
						if i.UsedPrice > 0 {
							usedPrice = fmt.Sprintf(" %.2f€", i.UsedPrice)
						}
					}
					bot.messageOpts(user, fmt.Sprintf("running %s %s %.2f€%s", key, link, price, usedPrice), false)
					return true
				})
				bot.log(fmt.Sprintf("elapsed: %s", bot.elapsed))
			case "stop":
				if args == "" {
					bot.message(user, "stop arguments not provided")
					continue
				}
				parsed, err := parseArgs(args, userChats[user])
				if err != nil {
					bot.message(user, err.Error())
				}
				if parsed.query == "*" {
					bot.stopAll()
					bot.message(user, "stopped all")
				} else {
					bot.stop(parsed)
					bot.message(user, fmt.Sprintf("stopped %s", parsed.id))
				}
			case "export":
				bot.export(user)
			case "batch":
				split := strings.Split(args, "\n")
				for _, s := range split {
					parsed, err := parseArgs(s, userChats[user])
					if err != nil {
						bot.message(user, err.Error())
					} else {
						bot.searchs.Store(parsed.id, nil)
					}
					bot.message(user, fmt.Sprintf("searching %s", parsed.id))
				}
			}
		}
	}
}

type parsedArgs struct {
	id    string
	chat  string
	query string
}

func parseArgs(args string, chat string) (parsedArgs, error) {
	split := strings.Split(args, "/")
	p := parsedArgs{
		chat:  chat,
		query: split[0],
	}
	switch len(split) {
	case 1:
	default:
		p.chat = split[0]
		p.query = split[1]
	}
	p.chat = strings.ToLower(strings.Trim(p.chat, " "))
	p.query = strings.ReplaceAll(strings.Trim(p.query, " "), " ", "+")
	p.id = fmt.Sprintf("%s/%s", p.chat, p.query)
	return p, nil
}

func (b *bot) search(ctx context.Context, parsed parsedArgs) {
	if parsed.query == "" {
		return
	}

	var item api.Item
	if err := b.db.Get("db", parsed.id, &item); err != nil {
		b.log(err)
	}
	if item.ID == "" {
		// store search with empty items on db
		if err := b.db.Put("db", parsed.id, item); err != nil {
			b.log(err)
			return
		}
		if err := b.client.Search(parsed.query, &item, func(api.Item) error { return nil }); err != nil {
			b.log(err)
			return
		}
	}
	if err := b.client.Search(parsed.query, &item, func(i api.Item) error {
		text := priceUsedMessage(i, parsed.chat)
		if i.PreviousPrice > i.Price {
			text = priceDownMessage(i, parsed.chat)
		}
		b.message(parsed.chat, text)
		return nil
	}); err != nil {
		b.log(err)
	}
	if item.ID == "" {
		return
	}
	if _, ok := b.searchs.Load(parsed.id); !ok {
		return
	}
	b.searchs.Store(parsed.id, item)
	if err := b.db.Put("db", parsed.id, item); err != nil {
		b.log(err)
		return
	}
}

func (b *bot) stopAll() {
	b.log("stopping all")
	var keys []string
	b.searchs.Range(func(k interface{}, _ interface{}) bool {
		keys = append(keys, k.(string))
		return true
	})
	for _, k := range keys {
		b.log(fmt.Sprintf("stopping %s", k))
		b.searchs.Delete(k)
		if err := b.db.Delete("db", k); err != nil {
			b.log(err)
		}
	}
}
func (b *bot) stop(parsed parsedArgs) {
	if _, ok := b.searchs.Load(parsed.id); ok {
		b.log(fmt.Sprintf("stopping %s", parsed.id))
		b.searchs.Delete(parsed.id)
		if err := b.db.Delete("db", parsed.id); err != nil {
			b.log(err)
		}
	}
}

func (b *bot) export(user int) {
	var keys []string
	b.searchs.Range(func(k interface{}, _ interface{}) bool {
		keys = append(keys, k.(string))
		return true
	})
	sort.Strings(keys)
	b.message(user, fmt.Sprintf("/batch %s", strings.Join(keys, "\n")))
}

func (b *bot) messageOpts(chat interface{}, text string, preview bool) {
	var msg tgbot.MessageConfig
	switch v := chat.(type) {
	case string:
		msg = tgbot.NewMessageToChannel(v, text)
	case int64:
		msg = tgbot.NewMessage(v, text)
	case int:
		msg = tgbot.NewMessage(int64(v), text)
	default:
		b.log(fmt.Sprintf("invalid type for message: %T", chat))
	}
	msg.DisableWebPagePreview = true
	if _, err := b.Send(msg); err != nil {
		b.log(fmt.Errorf("couldn't send message to %v: %w", chat, err))
	}
	<-time.After(100 * time.Millisecond)
}

func (b *bot) message(chat interface{}, text string) {
	b.messageOpts(chat, text, true)
}

func (b *bot) printChatID(msg *tgbot.Message) {
	if msg.Chat.IsPrivate() {
		return
	}
	newMembers := msg.NewChatMembers
	if newMembers != nil {
		for _, m := range *newMembers {
			if m.ID == b.Self.ID {
				admins, err := b.GetChatAdministrators(msg.Chat.ChatConfig())
				if err != nil {
					b.log(fmt.Errorf("couldn'r get admins for chat id %d: %w", msg.Chat.ID, err))
					return
				}
				for _, a := range admins {
					b.message(a.User.ID, fmt.Sprintf("bot added to %d %s %s", msg.Chat.ID, msg.Chat.Title, msg.Chat.UserName))
				}
			}
		}
	}
}

func (b *bot) log(obj interface{}) {
	text := fmt.Sprintf("%s", obj)
	log.Println(text)
	if _, err := b.Send(tgbot.NewMessage(int64(b.admin), text)); err != nil {
		log.Println(fmt.Errorf("couldn't send error to admin %d: %w", b.admin, err))
	}
	<-time.After(100 * time.Millisecond)
}

func priceDownMessage(i api.Item, chat string) string {
	bottom := ""
	if strings.HasPrefix(chat, "@") {
		bottom = fmt.Sprintf("\n\n📣 Más anuncios en %s", chat)
	}
	return fmt.Sprintf("⚡️ BAJADA DE PRECIO\n\n%s\n\n✅ Precio: %.2f€\n🚫 Anterior: %.2f€\n\n🔗 %s%s",
		i.Title, i.Price, i.PreviousPrice, i.Link, bottom)
}

func priceUsedMessage(i api.Item, chat string) string {
	bottom := ""
	if strings.HasPrefix(chat, "@") {
		bottom = fmt.Sprintf("\n\n📣 Más anuncios en %s", chat)
	}
	return fmt.Sprintf("♻️ REACONDICIONADO\n\n%s\n\n✅ Precio: %.2f€\n🚫 Nuevo: %.2f€\n\n🔗 %s%s",
		i.Title, i.UsedPrice, i.Price, i.Link, bottom)
}
