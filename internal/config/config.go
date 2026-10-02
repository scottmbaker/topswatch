package config

import (
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

type ServerConfig struct {
	Address string `yaml:"address"`
	Port    int    `yaml:"port"`
}

type CollectorConfig struct {
	Interval time.Duration `yaml:"interval"`
	History  int           `yaml:"history"`
	// ProcessRescan is how often the whole process table is walked: open
	// files are scanned for new GPU/NPU clients and the top-CPU process
	// list is refreshed. Between walks only known clients are re-read.
	// 0 walks on every sample (the pre-rescan behaviour).
	ProcessRescan time.Duration `yaml:"process_rescan"`
}

type CPUCollectorConfig struct {
	Enabled bool `yaml:"enabled"`
}

type NPUCollectorConfig struct {
	Enabled    bool   `yaml:"enabled"`
	DriverPath string `yaml:"driver_path,omitempty"`
}

type GPUCollectorConfig struct {
	Enabled bool `yaml:"enabled"`
}

type PowerCollectorConfig struct {
	Enabled bool `yaml:"enabled"`
}

type CollectorsConfig struct {
	CPU CPUCollectorConfig `yaml:"cpu"`
	GPU GPUCollectorConfig `yaml:"gpu"`
	NPU NPUCollectorConfig `yaml:"npu"`
	// Power collects RAPL energy domains and battery system power. It is
	// on by default and adapts to whatever the platform exposes.
	Power PowerCollectorConfig `yaml:"power"`
}

type Config struct {
	Server     ServerConfig    `yaml:"server"`
	Collector  CollectorConfig `yaml:"collector"`
	Collectors CollectorsConfig `yaml:"collectors"`
}

func Defaults() Config {
	return Config{
		Server: ServerConfig{
			Address: "0.0.0.0",
			Port:    9876,
		},
		Collector: CollectorConfig{
			Interval: 1 * time.Second,
			History:  300,
			ProcessRescan: 5 * time.Second,
		},
		Collectors: CollectorsConfig{
			CPU: CPUCollectorConfig{Enabled: true},
			GPU: GPUCollectorConfig{Enabled: true},
			NPU: NPUCollectorConfig{Enabled: true},
			Power: PowerCollectorConfig{Enabled: true},
		},
	}
}

func Load(path string) (Config, error) {
	cfg := Defaults()

	data, err := os.ReadFile(path)
	if err != nil {
		return cfg, err
	}

	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return cfg, err
	}

	return cfg, nil
}
