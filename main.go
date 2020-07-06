package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"

	"github.com/go-kit/kit/log"
	"github.com/go-kit/kit/log/level"
	kingpin "gopkg.in/alecthomas/kingpin.v2"

	"github.com/fengxsong/eureka-sd-adapter/pkg/adapter"
	"github.com/fengxsong/eureka-sd-adapter/pkg/config"
	"github.com/fengxsong/eureka-sd-adapter/pkg/eureka"
)

var (
	sda           *sdAdapters
	a             = kingpin.New("eureka adapter", "Tool to generate file_sd target files for eureka SD mechanisms.")
	output        = a.Flag("output", "The output directory for file_sd compatible file.").Default(".").String()
	configFile    = a.Flag("config.file", "Eureka SD adapter configuration file.").Default("eureka_sd_conf.yaml").String()
	listenAddress = a.Flag("web.listen-address", "The address to listen on for HTTP requests.").Default(":9146").String()
	kitLogger     = log.With(
		log.NewSyncLogger(log.NewLogfmtLogger(os.Stdout)),
		"time", log.DefaultTimestampUTC,
		"caller", log.DefaultCaller,
	)
)

type sdAdapters struct {
	m  map[*adapter.Adapter]struct{}
	sc *config.SafeConfig
	mu sync.Mutex
}

func (sda *sdAdapters) Start() {
	ctx := context.Background()
	for idx := range sda.sc.C.Configs {
		disc, err := eureka.NewDiscovery(sda.sc.C.Configs[idx], kitLogger)
		if err != nil {
			level.Error(kitLogger).Log("msg", "Failed to create eureka discovery", "err", err)
			continue
		}
		level.Info(kitLogger).Log("msg", "Eureka adapter running...")

		sdAdapter := adapter.NewAdapter(ctx, filepath.Join(*output, fmt.Sprintf("eureka_apps_%d.json", idx)), "eurekaSD", disc, kitLogger)
		sdAdapter.Run()
		sda.mu.Lock()
		sda.m[sdAdapter] = struct{}{}
		sda.mu.Unlock()
	}
	<-ctx.Done()
}

func (sda *sdAdapters) Stop() {
	for adp := range sda.m {
		adp.Stop()
		sda.mu.Lock()
		delete(sda.m, adp)
		sda.mu.Unlock()
	}
}

func (sda *sdAdapters) Reload() {
	sda.Stop()
	sda.Start()
}

func init() {
	sda = &sdAdapters{
		m: make(map[*adapter.Adapter]struct{}),
		sc: &config.SafeConfig{
			C: &config.SDConfigs{},
		},
	}
}

func main() {
	a.HelpFlag.Short('h')
	a.Parse(os.Args[1:])

	if err := sda.sc.ReloadConfig(*configFile); err != nil {
		level.Error(kitLogger).Log("msg", "Error loading config file", "err", err)
		os.Exit(1)
	}
	go sda.Start()

	// Reload Config File
	sigCh := make(chan os.Signal, 1)
	stopCh := make(chan struct{}, 1)
	reloadCh := make(chan chan error)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	go func() {
		for {
			select {
			case sig := <-sigCh:
				switch sig {
				case syscall.SIGHUP:
					level.Info(kitLogger).Log("msg", "Received reload by SIGHUP")
					if err := sda.sc.ReloadConfig(*configFile); err != nil {
						level.Error(kitLogger).Log("msg", "Error reload config by SIGHUP", "err", err)
					}
					go sda.Reload()
				case os.Interrupt, syscall.SIGTERM:
					level.Warn(kitLogger).Log("msg", "Received SIGTERM, exiting gracefully...")
					stopCh <- struct{}{}
				}
			case rc := <-reloadCh:
				level.Info(kitLogger).Log("msg", "Received reload request via web service")
				if err := sda.sc.ReloadConfig(*configFile); err != nil {
					level.Error(kitLogger).Log("msg", "Error Reload Config by http request", "err", err)
					rc <- err
				} else {
					go sda.Reload()
					rc <- nil
				}
			}
		}
	}()

	mux := http.NewServeMux()
	mux.HandleFunc("/-/reload", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" && r.Method != "PUT" {
			w.WriteHeader(http.StatusMethodNotAllowed)
			w.Write([]byte("This endpoint requires a POST/PUT request."))
			return
		}
		rc := make(chan error)
		defer close(rc)
		reloadCh <- rc
		if err := <-rc; err != nil {
			http.Error(w, fmt.Sprintf("Failed to reload config: %s", err), http.StatusInternalServerError)
			return
		}
	})
	s := &http.Server{
		Addr:    *listenAddress,
		Handler: mux,
	}
	go func() {
		level.Info(kitLogger).Log("HTTP Listenting on", *listenAddress)
		if err := s.ListenAndServe(); err != nil {
			level.Error(kitLogger).Log("msg", "Error to Start HTTP Server", "err", err)
			stopCh <- struct{}{}
		}
	}()

	<-stopCh
	s.Shutdown(context.Background())
	sda.Stop()
}
