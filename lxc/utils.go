package main

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"sort"
	"strings"
	"syscall"

	"github.com/lxc/lxd/client"
	"github.com/lxc/lxd/shared/api"
	"github.com/lxc/lxd/shared/i18n"
)

type stringList [][]string

func (a stringList) Len() int {
	return len(a)
}

func (a stringList) Swap(i, j int) {
	a[i], a[j] = a[j], a[i]
}

func (a stringList) Less(i, j int) bool {
	x := 0
	for x = range a[i] {
		if a[i][x] != a[j][x] {
			break
		}
	}

	if a[i][x] == "" {
		return false
	}

	if a[j][x] == "" {
		return true
	}

	return a[i][x] < a[j][x]
}

// Container name sorting
type byName [][]string

func (a byName) Len() int {
	return len(a)
}

func (a byName) Swap(i, j int) {
	a[i], a[j] = a[j], a[i]
}

func (a byName) Less(i, j int) bool {
	if a[i][0] == "" {
		return false
	}

	if a[j][0] == "" {
		return true
	}

	return a[i][0] < a[j][0]
}

// Batch operations
type batchResult struct {
	err  error
	name string
}

func runBatch(names []string, action func(name string) error) []batchResult {
	chResult := make(chan batchResult, len(names))

	for _, name := range names {
		go func(name string) {
			chResult <- batchResult{action(name), name}
		}(name)
	}

	results := []batchResult{}
	for range names {
		results = append(results, <-chResult)
	}

	return results
}

// summaryLine returns the first line of the help text. Conventionally, this
// should be a one-line command summary, potentially followed by a longer
// explanation.
func summaryLine(usage string) string {
	for _, line := range strings.Split(usage, "\n") {
		if strings.HasPrefix(line, "Usage:") {
			continue
		}

		if len(line) == 0 {
			continue
		}

		return strings.TrimSuffix(line, ".")
	}

	return i18n.G("Missing summary.")
}

// Used to return a user friendly error
func getLocalErr(err error) error {
	t, ok := err.(*url.Error)
	if !ok {
		return nil
	}

	u, ok := t.Err.(*net.OpError)
	if !ok {
		return nil
	}

	if u.Op == "dial" && u.Net == "unix" {
		var lxdErr error

		sysErr, ok := u.Err.(*os.SyscallError)
		if ok {
			lxdErr = sysErr.Err
		} else {
			// syscall.Errno may be returned on some systems, e.g. CentOS
			lxdErr, ok = u.Err.(syscall.Errno)
			if !ok {
				return nil
			}
		}

		switch lxdErr {
		case syscall.ENOENT, syscall.ECONNREFUSED, syscall.EACCES:
			return lxdErr
		}
	}

	return nil
}

// Create the specified image alises, updating those that already exist
func ensureImageAliases(client lxd.ContainerServer, aliases []api.ImageAlias, fingerprint string) error {
	if len(aliases) == 0 {
		return nil
	}

	names := make([]string, len(aliases))
	for i, alias := range aliases {
		names[i] = alias.Name
	}
	sort.Strings(names)

	resp, err := client.GetImageAliases()
	if err != nil {
		return err
	}

	// Delete existing aliases that match provided ones
	for _, alias := range GetExistingAliases(names, resp) {
		err := client.DeleteImageAlias(alias.Name)
		if err != nil {
			fmt.Println(fmt.Sprintf(i18n.G("Failed to remove alias %s"), alias.Name))
		}
	}
	// Create new aliases
	for _, alias := range aliases {
		aliasPost := api.ImageAliasesPost{}
		aliasPost.Name = alias.Name
		aliasPost.Target = fingerprint
		err := client.CreateImageAlias(aliasPost)
		if err != nil {
			fmt.Println(fmt.Sprintf(i18n.G("Failed to create alias %s"), alias.Name))
		}
	}
	return nil
}

// GetExistingAliases returns the intersection between a list of aliases and all the existing ones.
func GetExistingAliases(aliases []string, allAliases []api.ImageAliasesEntry) []api.ImageAliasesEntry {
	existing := []api.ImageAliasesEntry{}
	for _, alias := range allAliases {
		name := alias.Name
		pos := sort.SearchStrings(aliases, name)
		if pos < len(aliases) && aliases[pos] == name {
			existing = append(existing, alias)
		}
	}
	return existing
}
