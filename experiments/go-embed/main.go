package main

import (
	"fmt"
	"os"
	"time"
)

func main() {
	fmt.Println("Hello from Embedded Go!")
	fmt.Println("Arguments:", os.Args)
	// Don't wait for input in automated test environment, or use a timeout
	// fmt.Println("Press Enter to exit...")
	// fmt.Scanln()
	time.Sleep(100 * time.Millisecond)
}
