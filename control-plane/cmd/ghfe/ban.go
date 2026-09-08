// SPDX-License-Identifier: MIT

package main

// BannedEntities lists entity ids (org id or user id) denied the runner
// service. Resolve an id with `gh api orgs/<name> --jq '.id'`.
var BannedEntities = []int64{
	325883364, // github.com/demo99969
	325940811, // github.com/myjobs99
}

var BannedSenders = []int64{
	13410920, // github.com/quoc1506
}
