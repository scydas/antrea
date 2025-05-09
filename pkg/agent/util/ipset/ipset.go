// Copyright 2020 Antrea Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package ipset

import (
	"fmt"
	"regexp"
	"strings"

	"k8s.io/utils/exec"
)

type SetType string

const (
	// The hash:net set type uses a hash to store different sized IP network addresses.
	// The lookup time grows linearly with the number of the different prefix values added to the set.
	HashNet    SetType = "hash:net"
	HashIP     SetType = "hash:ip"
	HashIPPort SetType = "hash:ip,port"
)

// memberPattern is used to match the members part of ipset list result.
var memberPattern = regexp.MustCompile("(?m)^(.*\n)*Members:\n")

type Interface interface {
	CreateIPSet(name string, setType SetType, isIPv6 bool) error

	DestroyIPSet(name string) error

	AddEntry(name string, entry string) error

	DelEntry(name string, entry string) error

	ListEntries(name string) ([]string, error)
}

type Client struct {
	exec exec.Interface
}

var _ Interface = &Client{}

func NewClient() *Client {
	return &Client{
		exec: exec.New(),
	}
}

func (c *Client) DestroyIPSet(name string) error {
	cmd := c.exec.Command("ipset", "destroy", name)
	if output, err := cmd.CombinedOutput(); err != nil {
		if strings.Contains(string(output), "The set with the given name does not exist") {
			return nil
		}
		return fmt.Errorf("error destroying ipset %s, err: %w, output: %s", name, err, output)
	}
	return nil
}

// CreateIPSet creates a new set, it will ignore error when the set already exists.
func (c *Client) CreateIPSet(name string, setType SetType, isIPv6 bool) error {
	var cmd exec.Cmd
	if isIPv6 {
		// #nosec G204 -- inputs are not controlled by users
		cmd = c.exec.Command("ipset", "create", name, string(setType), "family", "inet6", "-exist")
	} else {
		// #nosec G204 -- inputs are not controlled by users
		cmd = c.exec.Command("ipset", "create", name, string(setType), "-exist")
	}
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("error creating ipset %s, err: %w, output: %s", name, err, output)
	}
	return nil
}

// AddEntry adds a new entry to the set, it will ignore error when the entry already exists.
func (c *Client) AddEntry(name string, entry string) error {
	cmd := c.exec.Command("ipset", "add", name, entry, "-exist")
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("error adding entry %s to ipset %s, err: %w, output: %s", entry, name, err, output)
	}
	return nil
}

// DelEntry deletes the entry from the set, it will ignore error when the entry doesn't exist.
func (c *Client) DelEntry(name string, entry string) error {
	cmd := c.exec.Command("ipset", "del", name, entry, "-exist")
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("error deleting entry %s from ipset %s, err: %w, output: %s", entry, name, err, output)
	}
	return nil
}

// ListEntries lists all the entries of the set.
func (c *Client) ListEntries(name string) ([]string, error) {
	cmd := c.exec.Command("ipset", "list", name)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("error listing ipset %s, err: %w, output: %s", name, err, output)
	}
	memberStr := memberPattern.ReplaceAllString(string(output), "")
	lines := strings.Split(memberStr, "\n")
	entries := make([]string, 0, len(lines))
	for i := range lines {
		if len(lines[i]) > 0 {
			entries = append(entries, lines[i])
		}
	}
	return entries, nil
}
