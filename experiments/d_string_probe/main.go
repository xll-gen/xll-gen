package main

import (
	"probe_experiment/generated"
)

type Service struct{}

func (s *Service) ProbeString(s_arg string) (string, error) {
	return "From Go: " + s_arg, nil
}

func main() {
	generated.Serve(&Service{})
}
