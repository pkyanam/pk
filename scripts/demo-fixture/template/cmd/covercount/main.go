package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"example.com/interval-cover/intervals"
)

type request struct {
	Ranges []intervals.Range `json:"ranges"`
}

func main() {
	if err := run(os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(input io.Reader, output io.Writer) error {
	var req request
	decoder := json.NewDecoder(input)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		return fmt.Errorf("decode input: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return fmt.Errorf("decode input: expected one JSON value")
	}
	count, err := intervals.CoveredCount(req.Ranges)
	if err != nil {
		return err
	}
	return json.NewEncoder(output).Encode(map[string]int{"covered": count})
}
