package service

import (
	"context"
	"reflect"
	"sync/atomic"
	"time"

	"github.com/vdaas/vald/internal/errgroup"
	"github.com/vdaas/vald/internal/errors"
	"github.com/vdaas/vald/internal/k8s"
	"github.com/vdaas/vald/internal/k8s/job"
	mpod "github.com/vdaas/vald/internal/k8s/metrics/pod"
	"github.com/vdaas/vald/internal/k8s/pod"
	"github.com/vdaas/vald/internal/k8s/statefulset"
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
	jobs         atomic.Value
	jobName      string
	jobNamespace string

	agentName      string
	agentNamespace string
	pods           atomic.Value
	podMetrics     atomic.Value

	statefulSets atomic.Value

	dcd  time.Duration // discover check duration
	eg   errgroup.Group
	ctrl k8s.Controller
}

// NewDiscoverer --
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
		job.WithOnReconcileFunc(func(jobList map[string][]job.Job) {
			jobs, ok := jobList[d.jobName]
			if ok {
				d.jobs.Store(jobs)
			} else {
				log.Infof("job not found: %s", d.jobName)
			}
		}),
	)
	if err != nil {
		return nil, err
	}

	ss, err := statefulset.New(
		statefulset.WithControllerName("statefulset discoverer"),
		statefulset.WithOnErrorFunc(func(err error) {
			log.Error(err)
		}),
		statefulset.WithOnReconcileFunc(func(statefulSetList map[string][]statefulset.StatefulSet) {
			sss, ok := statefulSetList[d.agentName]
			if ok {
				d.statefulSets.Store(sss)
			} else {
				log.Infof("statefuleset not found: %s", d.agentName)
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
		k8s.WithResourceController(ss), // statefulset controller
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
					mpods map[string]mpod.Pod
					pods  []pod.Pod
					jobs  []job.Job
					sss   []statefulset.StatefulSet
					ok    bool

					podModels         []*model.Pod
					jobModels         []*model.Job
					statefulSetModels []*model.StatefulSet
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

				jobs, ok = d.jobs.Load().([]job.Job)
				if !ok {
					log.Info("job is empty")
					continue
				}

				jobModels = make([]*model.Job, 0, len(jobs))
				for _, j := range jobs {
					var t time.Time
					if j.Status.StartTime != nil {
						t = j.Status.StartTime.Time
					}
					jobModels = append(jobModels, &model.Job{
						Name:      j.Name,
						Namespace: j.Namespace,
						Active:    j.Status.Active,
						StartTime: t,
					})
				}

				sss, ok = d.statefulSets.Load().([]statefulset.StatefulSet)
				if !ok {
					log.Info("statefulset is empty")
					continue
				}
				statefulSetModels = make([]*model.StatefulSet, 0, len(sss))
				for _, ss := range sss {
					statefulSetModels = append(statefulSetModels, &model.StatefulSet{
						Name:            ss.Name,
						Namespace:       ss.Namespace,
						DesiredReplicas: ss.Spec.Replicas,
						Replicas:        ss.Status.Replicas,
					})
				}

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