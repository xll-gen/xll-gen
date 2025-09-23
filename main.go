package main

import (
	"log"

	"github.com/xll-gen/xll-gen/cmd"
)

func main() {
	if err := cmd.Execute(); err != nil {
		log.Fatal(err)
	}
}
