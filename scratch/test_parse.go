package main

import (
	"fmt"
	"log"

	"Solis/internal/config"
)

func main() {
	cat, err := config.LoadCatalog("../catalog.json")
	if err != nil {
		log.Fatalf("Error: %v", err)
	}
	fmt.Printf("Loaded %d categories\n", len(cat))
}
