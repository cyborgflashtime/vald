package main

import (
	"context"

	"github.com/vdaas/vald/internal/info"
	"github.com/vdaas/vald/internal/log"
	"github.com/vdaas/vald/internal/runner"
	"github.com/vdaas/vald/internal/safety"
	"github.com/vdaas/vald/pkg/rebalancer/storage/controller/config"
	"github.com/vdaas/vald/pkg/rebalancer/storage/controller/usecase"
)

const (
	maxVersion = "v0.0.10"
	minVersion = "v0.0.0"
	name       = "vald agent rebalancer controller"
)

func main() {
	if err := safety.RecoverFunc(func() error {
		return runner.Do(
			context.Background(),
			runner.WithName(name),
			runner.WithVersion(info.Version, maxVersion, minVersion),
			runner.WithConfigLoader(func(path string) (interface{}, *config.GlobalConfig, error) {
				cfg, err := config.NewConfig(path)
				if err != nil {
					return nil, nil, err
				}
				return cfg, &cfg.GlobalConfig, nil
			}),
			runner.WithDaemonInitializer(func(cfg interface{}) (runner.Runner, error) {
				return usecase.New(cfg.(*config.Data))
			}),
		)
	})(); err != nil {
		log.Fatal(err, info.Get())
		return
	}
}