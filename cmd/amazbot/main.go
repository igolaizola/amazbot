package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strconv"

	"github.com/igolaizola/amazbot"
)

func main() {
	// Parse flags
	token := flag.String("token", "", "telegram bot token")
	db := flag.String("db", "amazbot.db", "database file path")
	python := flag.String("python", "python", "python command")
	admin := flag.Int("admin", 0, "admin chat id that controls the bot")
	var users arrayFlags
	flag.Var(&users, "user", "user chat id allowed to control the bot")

	flag.Parse()
	if *token == "" {
		log.Fatal("token not provided")
	}
	if *db == "" {
		log.Fatal("db not provided")
	}
	if *admin <= 0 {
		log.Fatal("admin provided")
	}

	// Create signal based context
	ctx, cancel := context.WithCancel(context.Background())
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, os.Kill)
	go func() {
		select {
		case <-c:
			cancel()
		case <-ctx.Done():
			cancel()
		}
		signal.Stop(c)
	}()

	// Run bot
	if err := amazbot.Run(ctx, *python, *token, *db, *admin, users); err != nil {
		log.Fatal(err)
	}
}

type arrayFlags []int

func (i *arrayFlags) String() string {
	if i == nil {
		return ""
	}
	return fmt.Sprintf("%v", []int(*i))
}

func (i *arrayFlags) Set(val string) error {
	num, err := strconv.Atoi(val)
	if err != nil {
		return fmt.Errorf("couldn't parse user %s: %w", val, err)
	}
	*i = append(*i, num)
	return nil
}
