package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/scottmbaker/topswatch/internal/collector"
	"github.com/scottmbaker/topswatch/internal/collectors/cpu"
	"github.com/scottmbaker/topswatch/internal/collectors/gpu"
	"github.com/scottmbaker/topswatch/internal/collectors/npu"
	"github.com/scottmbaker/topswatch/internal/config"
	"github.com/scottmbaker/topswatch/internal/module"
	"github.com/scottmbaker/topswatch/internal/textout"
	"github.com/scottmbaker/topswatch/internal/web"
)

func main() {
	configPath := flag.String("config", "topswatch.yaml", "path to config file")
	textMode := flag.Bool("text", false, "one-shot text output, then exit")
	address := flag.String("address", "", "override server bind address")
	port := flag.Int("port", 0, "override server port")
	interval := flag.Duration("interval", 0, "override poll interval")
	flag.Parse()

	// Load config
	cfg, err := config.Load(*configPath)
	if err != nil {
		if os.IsNotExist(err) {
			log.Printf("[config] %s not found, using defaults", *configPath)
			cfg = config.Defaults()
		} else {
			log.Fatalf("[config] failed to load %s: %v", *configPath, err)
		}
	}

	// CLI overrides
	if *address != "" {
		cfg.Server.Address = *address
	}
	if *port != 0 {
		cfg.Server.Port = *port
	}
	if *interval != 0 {
		cfg.Collector.Interval = *interval
	}

	// Init modules
	var modules []module.Module

	if cfg.Collectors.CPU.Enabled {
		cpuMod := cpu.New()
		if err := cpuMod.Init(); err != nil {
			log.Printf("[cpu] init failed: %v (continuing without CPU module)", err)
		} else {
			modules = append(modules, cpuMod)
			log.Printf("[cpu] %s", cpuMod.DeviceInfo().Name)
		}
	}

	if cfg.Collectors.GPU.Enabled {
		gpuMod := gpu.New()
		if err := gpuMod.Init(); err != nil {
			log.Printf("[gpu] init failed: %v (continuing without GPU module)", err)
		} else {
			modules = append(modules, gpuMod)
			info := gpuMod.DeviceInfo()
			log.Printf("[gpu] %s (%s)", info.Name, info.PCIDevice)
		}
	}

	if cfg.Collectors.NPU.Enabled {
		npuMod := npu.New(cfg.Collectors.NPU.DriverPath)
		if err := npuMod.Init(); err != nil {
			log.Printf("[npu] init failed: %v (continuing without NPU module)", err)
		} else {
			modules = append(modules, npuMod)
			info := npuMod.DeviceInfo()
			log.Printf("[npu] %s (%s)", info.Name, info.PCIDevice)
		}
	}

	if len(modules) == 0 {
		log.Fatal("no modules initialized — nothing to monitor")
	}

	coll := collector.New(modules, cfg.Collector.Interval, cfg.Collector.History)

	if *textMode {
		// Collect twice with a gap for delta-based metrics (utilization, power)
		coll.CollectOnce()
		time.Sleep(1 * time.Second)
		sample := coll.CollectOnce()
		textout.PrintSample(os.Stdout, sample)
		return
	}

	// Serve mode
	coll.Start()
	defer coll.Stop()

	srv := web.NewServer(coll, cfg.Server.Address, cfg.Server.Port)
	fmt.Printf("TopsWatch serving on http://%s:%d\n", cfg.Server.Address, cfg.Server.Port)
	fmt.Printf("  Web UI:     http://%s:%d/\n", cfg.Server.Address, cfg.Server.Port)
	fmt.Printf("  Prometheus: http://%s:%d/metrics\n", cfg.Server.Address, cfg.Server.Port)

	if err := srv.ListenAndServe(); err != nil {
		log.Fatalf("[web] %v", err)
	}
}
