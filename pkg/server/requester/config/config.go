/*
Copyright 2026 The llm-d Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

	http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package config

import (
	"github.com/spf13/pflag"

	"github.com/llm-d-incubation/llm-d-fast-model-actuation/pkg/server/requester/proxy"
)

// Config holds all configurable runtime parameters for the requester.
type Config struct {
	ProbesPort uint16
	SPIPort    uint16
	Proxy      proxy.ProxyConfig
}

// NewDefault returns a Config with sensible defaults.
func NewDefault() Config {
	return Config{
		ProbesPort: 8080,
		SPIPort:    8081,
		Proxy:      proxy.DefaultProxyConfig,
	}
}

// AddFlags registers command-line flags for all Config fields.
func (cfg *Config) AddFlags(fs *pflag.FlagSet) {
	fs.Uint16Var(&cfg.ProbesPort, "probes-port", cfg.ProbesPort, "port number for readiness/liveness probes")
	fs.Uint16Var(&cfg.SPIPort, "spi-port", cfg.SPIPort, "port for dual-pods SPI requests")
	cfg.Proxy.AddFlags(fs)
}
