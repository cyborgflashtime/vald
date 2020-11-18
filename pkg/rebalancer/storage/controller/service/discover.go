package service

import (
	"context"
	"reflect"
	"sync"
	"sync/atomic"
	"time"

	"github.com/vdaas/vald/internal/errgroup"
	"github.com/vdaas/vald/internal/errors"
	"github.com/vdaas/vald/internal/k8s"
	"github.com/vdaas/vald/internal/k8s/job"
	mpod "github.com/vdaas/vald/internal/k8s/metrics/pod"
	"github.com/vdaas/vald/internal/k8s/pod"
	"github.com/vdaas/vald/internal/log"
	"github.com/vdaas/vald/internal/safety"
	"github.com/vdaas/vald/pkg/rebalancer/storage/controller/model"
)

// Discoverer --
type Discoverer interface {
	// Start --
	Start(ctx context.Context) (<-chan error, error)
}

type discoverer struct {
	jobs         jobsMap
	jobsCache    atomic.Value
	jobName      string
	jobNamespace string

	agentName      string
	agentNamespace string
	pods           atomic.Value
	podMetrics     atomic.Value

	dcd  time.Duration // discover check duration
	eg   errgroup.Group
	ctrl k8s.Controller
}

func NewDiscoverer(opts ...DiscovererOption) (Discoverer, error) {
	d := new(discoverer)

	for _, opt := range append(defaultDiscovererOpts, opts...) {
		if err := opt(d); err != nil {
			return nil, errors.ErrOptionFailed(err, reflect.ValueOf(opt))
		}
	}

	job, err := job.New(
		job.WithControllerName("job discoverer"),
		job.WithOnErrorFunc(func(err error) {
			log.Error(err)
		}),
		job.WithOnReconcileFunc(func(jobsMap map[string][]job.Job) {
			for name, jobs := range jobsMap {
				if name == d.jobName {
					d.jobs.Store(name, jobs)
				}
			}
		}),
	)
	if err != nil {
		return nil, err
	}

	d.ctrl, err = k8s.New(
		k8s.WithControllerName("rebalance controller"),
		k8s.WithEnableLeaderElection(),
		k8s.WithResourceController(job),
		k8s.WithResourceController(pod.New(
			pod.WithControllerName("pod discover"),
			pod.WithOnErrorFunc(func(err error) {
				log.Error(err)
			}),
			pod.WithOnReconcileFunc(func(podList map[string][]pod.Pod) {
				pods, ok := podList[d.agentName]
				if ok {
					d.pods.Store(pods)
				} else {
					log.Infof("pod not found: %s", d.agentName)
				}
			}),
		)),
		k8s.WithResourceController(mpod.New(
			mpod.WithControllerName("pod metrics discover"),
			mpod.WithOnErrorFunc(func(err error) {
				log.Error(err)
			}),
			mpod.WithOnReconcileFunc(func(podList map[string]mpod.Pod) {
				if len(podList) > 0 {
					d.podMetrics.Store(podList)
				} else {
					log.Info("pod metrics not found")
				}
			}),
		)),
	)
	if err != nil {
		return nil, err
	}

	return d, nil
}

func (d *discoverer) Start(ctx context.Context) (<-chan error, error) {
	cech, err := d.ctrl.Start(ctx)
	if err != nil {
		return nil, err
	}

	ech := make(chan error, 1)
	d.eg.Go(safety.RecoverFunc(func() error {
		defer close(ech)
		dt := time.NewTicker(d.dcd)
		defer dt.Stop()
		for {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-dt.C:
				var (
					mpods     map[string]mpod.Pod
					pods      []pod.Pod
					ok        bool
					podModels []*model.Pod
				)

				mpods, ok = d.podMetrics.Load().(map[string]mpod.Pod)
				if !ok {
					log.Info("pod metrics is empty")
					continue
				}

				pods, ok = d.pods.Load().([]pod.Pod)
				if !ok {
					log.Info("pod is empty")
					continue
				}

				podModels = make([]*model.Pod, 0, len(pods))
				for _, p := range pods {
					if mpod, ok := mpods[p.Name]; ok {
						podModels = append(podModels, &model.Pod{
							Name:        p.Name,
							Namespace:   p.Namespace,
							MemoryLimit: p.MemLimit,
							MemoryUsage: mpod.Mem,
						})
					}
				}

				var wg sync.WaitGroup
				wg.Add(1)
				d.eg.Go(safety.RecoverFunc(func() error {
					defer wg.Done()
					jobs, ok := d.jobs.Load(d.jobName)
					if !ok {
						return nil
					}
					models := make([]*model.Job, 0, len(jobs))
					for _, job := range jobs {
						var t time.Time
						if job.Status.StartTime != nil {
							t = job.Status.StartTime.Time
						}
						models = append(models, &model.Job{
							Name:      job.Name,
							Namespace: job.Namespace,
							Active:    job.Status.Active,
							StartTime: t,
						})
					}
					d.jobsCache.Store(models)
					return nil
				}))
				wg.Wait()
			case err := <-cech:
				if err != nil {
					select {
					case <-ctx.Done():
						return ctx.Err()
					case ech <- err:
					}
				}
			}
		}
	}))

	return ech, nil
}
