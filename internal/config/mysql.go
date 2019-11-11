//
// Copyright (C) 2019 kpango (Yusuke Kato)
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//    http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
//

// Package config providers configuration type and load configuration logic
package config

type MySQL struct {
	DB   string `json:"db" yaml:"db"`
	Host string `json:"host" yaml:"host"`
	Port int    `json:"port" yaml:"port"`
	User string `json:"user" yaml:"user"`
	Pass string `json:"pass" yaml:"pass"`
	Name string `json:"name" yaml:"name"`
}

func (m *MySQL) Bind() *MySQL {
	m.DB = GetActualValue(m.DB)
	m.Host = GetActualValue(m.Host)
	m.User = GetActualValue(m.User)
	m.Pass = GetActualValue(m.Pass)
	m.Name = GetActualValue(m.Name)
	return m
}