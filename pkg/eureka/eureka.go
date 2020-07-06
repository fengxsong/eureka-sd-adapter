package eureka

import (
	"context"
	"fmt"
	"io/ioutil"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/ArthurHlt/go-eureka-client/eureka"
	"github.com/go-kit/kit/log"
	"github.com/go-kit/kit/log/level"
	"github.com/prometheus/common/model"
	"github.com/prometheus/prometheus/discovery/targetgroup"
	"github.com/sirupsen/logrus"

	"github.com/fengxsong/eureka-sd-adapter/pkg/config"
)

const (
	managementPort     = "management.port"
	metricsDisabled    = "metrics.disabled"
	metricsPort        = "metrics.port"
	metricsPath        = "metrics.path"
	defaultMetricsPath = "/metrics"
)

func init() {
	logrus.SetOutput(ioutil.Discard)
}

// Discovery provides service discovery based on a Eureka instance.
type Discovery struct {
	client          *eureka.Client
	servers         []string
	refreshInterval time.Duration
	lastRefresh     map[string]*targetgroup.Group
	labels          map[string]string
	logger          log.Logger
	incoming        chan struct{}
}

// NewDiscovery returns a new Eureka Service Discovery.
func NewDiscovery(conf *config.EurekaConfig, logger log.Logger) (*Discovery, error) {
	var (
		client *eureka.Client
		err    error
	)
	if len(conf.TLSConfig.CAFile) > 0 && len(conf.TLSConfig.CertFile) > 0 && len(conf.TLSConfig.KeyFile) > 0 {
		client, err = eureka.NewTLSClient(conf.EurekaServer, conf.TLSConfig.CertFile, conf.TLSConfig.KeyFile, []string{conf.TLSConfig.CAFile})
		if err != nil {
			return nil, err
		}
	} else {
		client = eureka.NewClient(conf.EurekaServer)
	}

	return &Discovery{
		client:          client,
		servers:         conf.EurekaServer,
		refreshInterval: time.Duration(conf.RefreshInterval),
		labels:          conf.Labels,
		logger:          logger,
		incoming:        make(chan struct{}, 1<<4),
	}, nil

}

// Run implements the TargetProvider interface.
func (d *Discovery) Run(ctx context.Context, ch chan<- []*targetgroup.Group) {
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-d.incoming:
				err := d.updateServices(ctx, ch)
				if err != nil {
					level.Error(d.logger).Log("msg", "Error while updating services", "err", err)
				}
			case <-time.After(d.refreshInterval):
				level.Info(d.logger).Log("msg", fmt.Sprintf("refresh service after %s", d.refreshInterval))
				d.incoming <- struct{}{}
			}
		}
	}()
	d.incoming <- struct{}{}
}

func (d *Discovery) updateServices(ctx context.Context, ch chan<- []*targetgroup.Group) (err error) {
	targetMap, err := d.fetchTargetGroups()

	if err != nil {
		return err
	}

	all := make([]*targetgroup.Group, 0, len(targetMap))
	for _, tg := range targetMap {
		all = append(all, tg)
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case ch <- all:
	}

	// Remove services which did disappear.
	for source := range d.lastRefresh {
		_, ok := targetMap[source]
		if !ok {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case ch <- []*targetgroup.Group{{Source: source}}:
				level.Debug(d.logger).Log("msg", "Removing group", "source", source)
			}
		}
	}

	d.lastRefresh = targetMap
	return nil
}

func (d *Discovery) fetchTargetGroups() (map[string]*targetgroup.Group, error) {
	// get eureka applications
	apps, err := d.client.GetApplications()

	if err != nil {
		return nil, err
	}

	groups := d.createAppsToTargetGroups(apps)
	return groups, nil
}

// createAppsToTargetGroups converts Eureka Applications into target groups.
func (d *Discovery) createAppsToTargetGroups(apps *eureka.Applications) map[string]*targetgroup.Group {
	tgroups := map[string]*targetgroup.Group{}
	for _, app := range apps.Applications {
		group := d.createTargetGroup(&app)
		tgroups[group.Source] = group
	}

	return tgroups
}

func (d *Discovery) createTargetGroup(app *eureka.Application) *targetgroup.Group {
	targets := d.createTargetsFromApp(app)
	appName := strings.Replace(strings.ToLower(app.Name), " ", "_", -1)
	jobLabelname := model.LabelValue(appName)

	tg := &targetgroup.Group{
		Targets: targets,
		Labels: model.LabelSet{
			model.JobLabel: jobLabelname,
		},
		Source: appName,
	}

	// appName label
	tg.Labels[model.LabelName("appName")] = model.LabelValue(appName)
	// set additional labelset
	for ln, lv := range d.labels {
		tg.Labels[model.LabelName(ln)] = model.LabelValue(lv)
	}

	return tg
}

func (d *Discovery) createTargetsFromApp(app *eureka.Application) []model.LabelSet {
	targets := make([]model.LabelSet, 0)
	for _, t := range app.Instances {
		if v, ok := t.Metadata.Map[metricsDisabled]; ok {
			if disabled, _ := strconv.ParseBool(v); disabled {
				continue
			}
		}
		targetAddress := createTargetFromAppInstance(&t)
		target := model.LabelSet{
			model.AddressLabel: model.LabelValue(targetAddress),
		}
		targets = append(targets, target)
	}
	return targets
}

func getMetricsPathFromAppInstance(instance *eureka.InstanceInfo) string {
	if v, ok := instance.Metadata.Map[metricsPath]; ok && len(v) > 0 {
		return v
	}
	return defaultMetricsPath
}

func createTargetFromAppInstance(instance *eureka.InstanceInfo) string {
	// create a target from a Eureka app instance
	// target = {__address__="hostname:port or hostname:management.port"}
	for ln, lv := range instance.Metadata.Map {
		if strings.Contains(ln, metricsPort) {
			return net.JoinHostPort(instance.HostName, lv)
		} else if strings.Contains(ln, managementPort) {
			return net.JoinHostPort(instance.HostName, lv)
		}
	}
	return net.JoinHostPort(instance.HostName, fmt.Sprintf("%d", instance.Port.Port))
}
