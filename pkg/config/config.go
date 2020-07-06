package config

import (
	"fmt"
	"io/ioutil"
	"strings"
	"sync"

	prom_config "github.com/prometheus/common/config"
	prom_model "github.com/prometheus/common/model"
	yaml "gopkg.in/yaml.v2"
)

// SDConfigs eureka servers configuration
type SDConfigs struct {
	Configs []*EurekaConfig `yaml:"eureka_sd_adapter_configs"`

	XXX map[string]interface{} `yaml:",inline"`
}

// UnmarshalYAML implements the yaml.Unmarshaler interface.
func (c *SDConfigs) UnmarshalYAML(unmarshal func(interface{}) error) error {
	type plain SDConfigs
	if err := unmarshal((*plain)(c)); err != nil {
		return err
	}
	if err := checkOverflow(c.XXX, "SDConfigs"); err != nil {
		return err
	}
	return nil
}

// EurekaConfig a eureka server configuration
type EurekaConfig struct {
	EurekaServer    []string              `yaml:"eureka_server"`
	Timeout         prom_model.Duration   `yaml:"timeout,omitempty"`
	RefreshInterval prom_model.Duration   `yaml:"refresh_interval,omitempty"`
	TLSConfig       prom_config.TLSConfig `yaml:"tls_config,omitempty"`
	Labels          map[string]string     `yaml:"labels,omitempty"`

	XXX map[string]interface{} `yaml:",inline"`
}

// UnmarshalYAML implements the yaml.Unmarshaler interface.
func (c *EurekaConfig) UnmarshalYAML(unmarshal func(interface{}) error) error {
	type plain EurekaConfig
	if err := unmarshal((*plain)(c)); err != nil {
		return err
	}
	if err := checkOverflow(c.XXX, "eurekaConfig"); err != nil {
		return err
	}
	return nil
}

func checkOverflow(m map[string]interface{}, ctx string) error {
	if len(m) > 0 {
		var keys []string
		for k := range m {
			keys = append(keys, k)
		}
		return fmt.Errorf("unknown fields in %s: %s", ctx, strings.Join(keys, ", "))
	}
	return nil
}

// SafeConfig - mutex protected config for live reloads.
type SafeConfig struct {
	sync.RWMutex
	C *SDConfigs
}

// ReloadConfig - allows for live reloads of the configuration file.
func (sc *SafeConfig) ReloadConfig(confFile string) (err error) {
	var c = &SDConfigs{}

	yamlFile, err := ioutil.ReadFile(confFile)
	if err != nil {
		return fmt.Errorf("Error reading config file: %s", err)
	}

	if err := yaml.Unmarshal(yamlFile, c); err != nil {
		return fmt.Errorf("Error parsing config file: %s", err)
	}

	sc.Lock()
	sc.C = c
	sc.Unlock()

	return nil
}
