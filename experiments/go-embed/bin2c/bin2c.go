package main

import (
	"fmt"
	"io/ioutil"
	"os"
)

func main() {
	if len(os.Args) < 3 {
		fmt.Println("Usage: bin2c <input> <output_header>")
		os.Exit(1)
	}

	inputFile := os.Args[1]
	outputFile := os.Args[2]

	data, err := ioutil.ReadFile(inputFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error reading input: %v\n", err)
		os.Exit(1)
	}

	f, err := os.Create(outputFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating output: %v\n", err)
		os.Exit(1)
	}
	defer f.Close()

	fmt.Fprintf(f, "#ifndef EMBEDDED_DATA_H\n#define EMBEDDED_DATA_H\n\n")
	fmt.Fprintf(f, "const unsigned char guest_zst[] = {")
	for i, b := range data {
		if i%12 == 0 {
			fmt.Fprintf(f, "\n    ")
		}
		fmt.Fprintf(f, "0x%02x, ", b)
	}
	fmt.Fprintf(f, "\n};\n\n")
	fmt.Fprintf(f, "const unsigned long guest_zst_len = %d;\n\n", len(data))
	fmt.Fprintf(f, "#endif // EMBEDDED_DATA_H\n")
}
