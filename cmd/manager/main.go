package main

import (
	"context"
	"log"

	"github.com/pax-beehive/pax-manager/internal/manager"
)

func main() {
	if err := manager.Run(context.Background()); err != nil {
		log.Fatal(err)
	}
}
