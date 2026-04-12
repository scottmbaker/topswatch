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

type CollectorsConfig struct {
	CPU CPUCollectorConfig `yaml:"cpu"`
	GPU GPUCollectorConfig `yaml:"gpu"`
	NPU NPUCollectorConfig `yaml:"npu"`
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
		},
		Collectors: CollectorsConfig{
			CPU: CPUCollectorConfig{Enabled: true},
			GPU: GPUCollectorConfig{Enabled: true},
			NPU: NPUCollectorConfig{Enabled: true},
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
