package config

import (
	"fmt"
)

type Config struct {
	Project   ProjectConfig `yaml:"project"`
	Gen       GenConfig     `yaml:"gen"`
	Server    ServerConfig  `yaml:"server"`
	Functions []Function    `yaml:"functions"`
	Events    []Event       `yaml:"events"`
}

type Event struct {
	Type        string `yaml:"type"`
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
}

type ServerConfig struct {
	Timeout string `yaml:"timeout"`
	Workers int    `yaml:"workers"`
}

type ProjectConfig struct {
	Name    string `yaml:"name"`
	Version string `yaml:"version"`
}

type GenConfig struct {
	Go               GoConfig `yaml:"go"`
	DisablePidSuffix bool     `yaml:"disable_pid_suffix"`
}

type GoConfig struct {
	Package string `yaml:"package"`
}

type Function struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	Args        []Arg  `yaml:"args"`
	Return      string `yaml:"return"`
	Volatile    bool   `yaml:"volatile"`
	Async       bool   `yaml:"async"`
	Category    string `yaml:"category"`
	Shortcut    string `yaml:"shortcut"`
	HelpTopic   string `yaml:"help_topic"`
	Timeout     string `yaml:"timeout"`
	Caller      bool   `yaml:"caller"`
}

type Arg struct {
	Name        string `yaml:"name"`
	Type        string `yaml:"type"`
	Description string `yaml:"description"`
}

func Validate(config *Config) error {
	seenEvents := make(map[string]bool)
	for _, evt := range config.Events {
		if seenEvents[evt.Type] {
			return fmt.Errorf("duplicate event type: %s", evt.Type)
		}
		seenEvents[evt.Type] = true
	}
	return nil
}
